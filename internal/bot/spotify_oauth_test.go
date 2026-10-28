package bot

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"discord-music-bot/internal/spotify"
)

func TestSpotifyOAuthHandlersUseLoopbackAuthorizationFlow(t *testing.T) {
	client, err := spotify.NewWithUserOAuth(
		"client-id",
		"client-secret",
		"http://127.0.0.1:18080/spotify/callback",
		filepath.Join(t.TempDir(), "spotify-token.json"),
	)
	if err != nil {
		t.Fatalf("NewWithUserOAuth() error = %v", err)
	}
	application := &Bot{spotify: client, logger: slog.Default()}
	handler := application.healthHandler()

	loginRequest := httptest.NewRequest(http.MethodGet, "/spotify/login", nil)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusFound {
		t.Fatalf("GET /spotify/login status = %d, want %d", loginResponse.Code, http.StatusFound)
	}
	location, err := url.Parse(loginResponse.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Spotify authorization redirect: %v", err)
	}
	if location.Host != "accounts.spotify.com" || location.Query().Get("state") == "" {
		t.Fatalf("Spotify authorization redirect = %q, want Spotify URL with state", location)
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "/spotify/callback", nil)
	callbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(callbackResponse, callbackRequest)
	if callbackResponse.Code != http.StatusBadRequest {
		t.Fatalf("GET /spotify/callback without parameters status = %d, want %d", callbackResponse.Code, http.StatusBadRequest)
	}
}
