package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpotifyResource(t *testing.T) {
	tests := []struct {
		input         string
		wantType      string
		wantID        string
		wantIsSpotify bool
	}{
		{"https://open.spotify.com/track/track-id?si=example", "track", "track-id", true},
		{"https://open.spotify.com/playlist/list-id", "playlist", "list-id", true},
		{"spotify:track:track-id", "track", "track-id", true},
		{"https://youtube.com/watch?v=video-id", "", "", false},
	}
	for _, test := range tests {
		resourceType, id, ok := parseSpotifyResource(test.input)
		if resourceType != test.wantType || id != test.wantID || ok != test.wantIsSpotify {
			t.Errorf("parseSpotifyResource(%q) = (%q, %q, %t), want (%q, %q, %t)",
				test.input, resourceType, id, ok, test.wantType, test.wantID, test.wantIsSpotify)
		}
	}
}

func TestGetJSONRejectsUnexpectedHost(t *testing.T) {
	client := New("id", "secret")
	err := client.getJSON(context.Background(), "https://attacker.example/resource", "token", &struct{}{})
	if err == nil {
		t.Fatal("getJSON() error = nil, want unexpected endpoint error")
	}
}

func TestPublicPlaylistMetadataIsBoundedAndParsed(t *testing.T) {
	client := New("id", "secret")
	client.httpClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != publicPlaylistEndpoint+"list-id" {
			t.Fatalf("public playlist URL = %q", request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"state":{"data":{"entity":{"trackList":[{"title":"Song","subtitle":"Artist"}]}}}}}}</script>`))}, nil
	})}
	tracks, err := client.publicPlaylistTracks(context.Background(), "list-id")
	if err != nil {
		t.Fatalf("publicPlaylistTracks() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0] != (Track{Name: "Song", Artist: "Artist"}) {
		t.Fatalf("publicPlaylistTracks() = %+v", tracks)
	}
}

func TestGetJSONPreservesForbiddenStatus(t *testing.T) {
	client := New("id", "secret")
	client.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusForbidden, `{"error":"forbidden"}`), nil
	})}
	err := client.getJSON(context.Background(), apiEndpoint+"/playlists/list-id/items?limit=100", "token", &struct{}{})
	var apiErr *apiStatusError
	if !errors.As(err, &apiErr) || apiErr.statusCode != http.StatusForbidden {
		t.Fatalf("getJSON() error = %v, want typed 403", err)
	}
}

func TestPlaylistRequiresUserAuthorization(t *testing.T) {
	client := New("id", "secret")
	_, handled, err := client.Resolve(context.Background(), "https://open.spotify.com/playlist/list-id")
	if !handled || err == nil || !strings.Contains(err.Error(), "user authorization") {
		t.Fatalf("Resolve() = (handled %t, error %v), want user authorization error", handled, err)
	}
}

func TestUserAuthorizationPersistsRefreshTokenAndReadsPlaylistItems(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "spotify", "token.json")
	client, err := NewWithUserOAuth("client-id", "client-secret", "http://127.0.0.1:18080/spotify/callback", tokenFile)
	if err != nil {
		t.Fatalf("NewWithUserOAuth() error = %v", err)
	}
	var getEndpoint string
	client.httpClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		switch request.Method {
		case http.MethodPost:
			if request.URL.String() != tokenEndpoint {
				t.Fatalf("token endpoint = %q, want %q", request.URL.String(), tokenEndpoint)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse token request form: %v", err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("redirect_uri") != client.redirectURI {
				t.Fatalf("token request form = %v, want authorization code grant and redirect URI", request.Form)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`), nil
		case http.MethodGet:
			getEndpoint = request.URL.String()
			if request.Header.Get("Authorization") != "Bearer access" {
				t.Fatalf("Authorization = %q, want user access token", request.Header.Get("Authorization"))
			}
			return jsonResponse(http.StatusOK, `{"items":[{"item":{"name":"Track title","artists":[{"name":"Artist"}]}}],"next":null}`), nil
		default:
			t.Fatalf("unexpected HTTP method %q", request.Method)
			return nil, nil
		}
	})}

	authorizationURL, err := client.BeginUserAuthorization()
	if err != nil {
		t.Fatalf("BeginUserAuthorization() error = %v", err)
	}
	parsedAuthorizationURL, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	if parsedAuthorizationURL.Query().Get("scope") != playlistScopes {
		t.Fatalf("authorization scopes = %q, want %q", parsedAuthorizationURL.Query().Get("scope"), playlistScopes)
	}
	if parsedAuthorizationURL.Query().Get("redirect_uri") != client.redirectURI {
		t.Fatalf("authorization redirect URI = %q, want %q", parsedAuthorizationURL.Query().Get("redirect_uri"), client.redirectURI)
	}
	if err := client.CompleteUserAuthorization(context.Background(), "auth-code", parsedAuthorizationURL.Query().Get("state")); err != nil {
		t.Fatalf("CompleteUserAuthorization() error = %v", err)
	}
	tracks, handled, err := client.Resolve(context.Background(), "https://open.spotify.com/playlist/list-id")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !handled || len(tracks) != 1 || tracks[0].Name != "Track title" || tracks[0].Artist != "Artist" {
		t.Fatalf("Resolve() = (%v, %t), want one playlist track", tracks, handled)
	}
	if !strings.HasSuffix(getEndpoint, "/v1/playlists/list-id/items?limit=100") {
		t.Fatalf("playlist items endpoint = %q, want current items endpoint", getEndpoint)
	}
	persisted, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read persisted refresh token: %v", err)
	}
	var saved struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatalf("decode persisted refresh token: %v", err)
	}
	if saved.RefreshToken != "refresh" {
		t.Fatalf("persisted refresh token = %q, want token value", saved.RefreshToken)
	}
}

func TestUserAuthorizationRejectsInvalidState(t *testing.T) {
	client, err := NewWithUserOAuth("id", "secret", "http://127.0.0.1:18080/spotify/callback", filepath.Join(t.TempDir(), "token.json"))
	if err != nil {
		t.Fatalf("NewWithUserOAuth() error = %v", err)
	}
	if _, err := client.BeginUserAuthorization(); err != nil {
		t.Fatalf("BeginUserAuthorization() error = %v", err)
	}
	if err := client.CompleteUserAuthorization(context.Background(), "code", "wrong-state"); err == nil {
		t.Fatal("CompleteUserAuthorization() error = nil, want state validation error")
	}
}

func TestNewWithUserOAuthRequiresLoopbackCallback(t *testing.T) {
	for _, redirectURI := range []string{
		"https://example.test/spotify/callback",
		"http://0.0.0.0:18080/spotify/callback",
		"http://127.0.0.1/spotify/callback",
		"http://127.0.0.1:18080/other",
	} {
		if _, err := NewWithUserOAuth("id", "secret", redirectURI, filepath.Join(t.TempDir(), "token.json")); err == nil {
			t.Errorf("NewWithUserOAuth(%q) error = nil, want callback validation error", redirectURI)
		}
	}
}

func TestLoadedRefreshTokenIsRenewedAndUsedForPlaylist(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "spotify", "token.json")
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	if err := os.WriteFile(tokenFile, []byte(`{"refresh_token":"saved-refresh"}`), 0600); err != nil {
		t.Fatalf("write existing refresh token: %v", err)
	}
	client, err := NewWithUserOAuth("client-id", "client-secret", "http://127.0.0.1:18080/spotify/callback", tokenFile)
	if err != nil {
		t.Fatalf("NewWithUserOAuth() error = %v", err)
	}
	client.httpClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse refresh request form: %v", err)
			}
			if request.Form.Get("grant_type") != "refresh_token" || request.Form.Get("refresh_token") != "saved-refresh" {
				t.Fatalf("refresh request form = %v, want saved refresh token grant", request.Form)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"renewed-access","expires_in":3600}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer renewed-access" {
			t.Fatalf("Authorization = %q, want renewed user access token", request.Header.Get("Authorization"))
		}
		return jsonResponse(http.StatusOK, `{"items":[{"item":{"name":"Playlist song","artists":[{"name":"Artist"}]}}],"next":null}`), nil
	})}
	tracks, handled, err := client.Resolve(context.Background(), "https://open.spotify.com/playlist/list-id")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !handled || len(tracks) != 1 || tracks[0].Name != "Playlist song" {
		t.Fatalf("Resolve() = (%v, %t), want playlist song", tracks, handled)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
