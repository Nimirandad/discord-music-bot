package spotify

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	authorizationEndpoint  = "https://accounts.spotify.com/authorize"
	tokenEndpoint          = "https://accounts.spotify.com/api/token"
	apiEndpoint            = "https://api.spotify.com/v1"
	publicPlaylistEndpoint = "https://open.spotify.com/embed/playlist/"
	maxTracks              = 500
	maxPublicTracks        = 100
	maxPublicPageBytes     = 2 << 20
	stateTTL               = 10 * time.Minute
	playlistScopes         = "playlist-read-private playlist-read-collaborative"
)

type Track struct {
	Name   string
	Artist string
}

type Client struct {
	clientID     string
	clientSecret string
	redirectURI  string
	tokenFile    string
	httpClient   *http.Client

	mu                 sync.Mutex
	accessToken        string
	refreshToken       string
	expiresAt          time.Time
	accessTokenIsUser  bool
	pendingAuthState   string
	pendingAuthExpires time.Time
}

func New(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

func NewWithUserOAuth(clientID, clientSecret, redirectURI, tokenFile string) (*Client, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/spotify/callback" {
		return nil, fmt.Errorf("SPOTIFY_REDIRECT_URI must be a loopback URL like http://127.0.0.1:18080/spotify/callback")
	}
	if tokenFile == "" {
		return nil, fmt.Errorf("Spotify OAuth token file path cannot be empty")
	}
	client := New(clientID, clientSecret)
	client.redirectURI = redirectURI
	client.tokenFile = tokenFile
	persisted, err := os.ReadFile(tokenFile)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read Spotify OAuth token file: %w", err)
	}
	if err == nil {
		var saved struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(persisted, &saved); err != nil {
			return nil, fmt.Errorf("decode Spotify OAuth token file: %w", err)
		}
		if saved.RefreshToken == "" {
			return nil, fmt.Errorf("Spotify OAuth token file does not contain a refresh token")
		}
		client.refreshToken = saved.RefreshToken
	}
	return client, nil
}

func (c *Client) Resolve(ctx context.Context, input string) ([]Track, bool, error) {
	resourceType, id, ok := parseSpotifyResource(input)
	if !ok {
		return nil, false, nil
	}
	userTokenRequired := resourceType == "playlist"
	token, err := c.token(ctx, userTokenRequired)
	if err != nil {
		return nil, true, err
	}
	switch resourceType {
	case "track":
		var item apiTrack
		if err := c.getJSON(ctx, apiEndpoint+"/tracks/"+url.PathEscape(id), token, &item); err != nil {
			return nil, true, err
		}
		return []Track{toTrack(item)}, true, nil
	case "playlist":
		tracks, err := c.playlistTracks(ctx, id, token)
		var apiErr *apiStatusError
		if errors.As(err, &apiErr) && apiErr.statusCode == http.StatusForbidden {
			tracks, err = c.publicPlaylistTracks(ctx, id)
		}
		return tracks, true, err
	default:
		return nil, true, fmt.Errorf("Spotify %s links are not supported; use a track or playlist link", resourceType)
	}
}

func (c *Client) BeginUserAuthorization() (string, error) {
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("generate Spotify OAuth state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	c.mu.Lock()
	c.pendingAuthState = state
	c.pendingAuthExpires = time.Now().Add(stateTTL)
	c.mu.Unlock()

	authorizationURL, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse Spotify authorization endpoint: %w", err)
	}
	query := authorizationURL.Query()
	query.Set("client_id", c.clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", c.redirectURI)
	query.Set("scope", playlistScopes)
	query.Set("state", state)
	authorizationURL.RawQuery = query.Encode()
	return authorizationURL.String(), nil
}

func (c *Client) CompleteUserAuthorization(ctx context.Context, code, state string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	expectedState := c.pendingAuthState
	expiresAt := c.pendingAuthExpires
	c.pendingAuthState = ""
	c.pendingAuthExpires = time.Time{}
	if expectedState == "" || state == "" || state != expectedState || time.Now().After(expiresAt) {
		return fmt.Errorf("Spotify authorization state is invalid or expired; start authorization again")
	}
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {c.redirectURI},
	}
	return c.exchangeUserToken(ctx, form, "")
}

func (c *Client) token(ctx context.Context, userTokenRequired bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Until(c.expiresAt) > time.Minute && (!userTokenRequired || c.accessTokenIsUser) {
		return c.accessToken, nil
	}

	if c.refreshToken != "" {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {c.refreshToken},
		}
		if err := c.exchangeUserToken(ctx, form, c.refreshToken); err != nil {
			return "", err
		}
		return c.accessToken, nil
	}
	if userTokenRequired {
		return "", fmt.Errorf("Spotify playlists require user authorization; open the bot's /spotify/login endpoint through the SSH tunnel first")
	}

	form := url.Values{"grant_type": {"client_credentials"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create Spotify token request: %w", err)
	}
	request.SetBasicAuth(c.clientID, c.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request Spotify token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("Spotify token API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("decode Spotify token response: %w", err)
	}
	if tokenResponse.AccessToken == "" || tokenResponse.ExpiresIn < 1 {
		return "", fmt.Errorf("Spotify returned an invalid access token")
	}
	c.accessToken = tokenResponse.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	c.accessTokenIsUser = false
	return c.accessToken, nil
}

func (c *Client) exchangeUserToken(ctx context.Context, form url.Values, existingRefreshToken string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create Spotify user token request: %w", err)
	}
	request.SetBasicAuth(c.clientID, c.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request Spotify user token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Spotify user token API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		return fmt.Errorf("decode Spotify user token response: %w", err)
	}
	if tokenResponse.AccessToken == "" || tokenResponse.ExpiresIn < 1 {
		return fmt.Errorf("Spotify returned an invalid user access token")
	}
	refreshToken := tokenResponse.RefreshToken
	if refreshToken == "" {
		refreshToken = existingRefreshToken
	}
	if refreshToken == "" {
		return fmt.Errorf("Spotify authorization response did not include a refresh token")
	}
	if err := c.saveRefreshToken(refreshToken); err != nil {
		return err
	}
	c.accessToken = tokenResponse.AccessToken
	c.refreshToken = refreshToken
	c.expiresAt = time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	c.accessTokenIsUser = true
	return nil
}

func (c *Client) saveRefreshToken(refreshToken string) error {
	if err := os.MkdirAll(filepath.Dir(c.tokenFile), 0700); err != nil {
		return fmt.Errorf("create Spotify token directory: %w", err)
	}
	data, err := json.Marshal(struct {
		RefreshToken string `json:"refresh_token"`
	}{RefreshToken: refreshToken})
	if err != nil {
		return fmt.Errorf("encode Spotify refresh token: %w", err)
	}
	if err := os.WriteFile(c.tokenFile, data, 0600); err != nil {
		return fmt.Errorf("persist Spotify refresh token: %w", err)
	}
	if err := os.Chmod(c.tokenFile, 0600); err != nil {
		return fmt.Errorf("secure Spotify refresh token file: %w", err)
	}
	return nil
}

func (c *Client) playlistTracks(ctx context.Context, id, token string) ([]Track, error) {
	next := apiEndpoint + "/playlists/" + url.PathEscape(id) + "/items?limit=100"
	tracks := make([]Track, 0)
	for next != "" && len(tracks) < maxTracks {
		var page playlistPage
		if err := c.getJSON(ctx, next, token, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			track := item.Item
			if track.Name == "" {
				track = item.Track
			}
			if track.Name != "" {
				tracks = append(tracks, toTrack(track))
				if len(tracks) == maxTracks {
					break
				}
			}
		}
		next = page.Next
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("Spotify playlist contains no playable tracks")
	}
	return tracks, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint, token string, destination any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse Spotify API endpoint: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() != "api.spotify.com" || parsed.User != nil {
		return fmt.Errorf("refusing unexpected Spotify API endpoint %q", endpoint)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Spotify API request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request Spotify API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &apiStatusError{statusCode: response.StatusCode, message: fmt.Sprintf("Spotify API returned %s for %s: %s", response.Status, parsed.Path, strings.TrimSpace(string(body)))}
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode Spotify API response: %w", err)
	}
	return nil
}

type apiStatusError struct {
	statusCode int
	message    string
}

func (e *apiStatusError) Error() string { return e.message }

// publicPlaylistTracks reads only the public embed document. Spotify may omit
// private/custom playlists or expose only a partial list; this is best-effort metadata.
func (c *Client) publicPlaylistTracks(ctx context.Context, id string) ([]Track, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, publicPlaylistEndpoint+url.PathEscape(id), nil)
	if err != nil {
		return nil, fmt.Errorf("create Spotify public playlist request: %w", err)
	}
	request.Header.Set("Accept", "text/html")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Spotify public playlist metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Spotify public playlist page returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPublicPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Spotify public playlist metadata: %w", err)
	}
	if len(body) > maxPublicPageBytes {
		return nil, fmt.Errorf("Spotify public playlist page exceeds %d bytes", maxPublicPageBytes)
	}
	const marker = `<script id="__NEXT_DATA__" type="application/json">`
	page := string(body)
	start := strings.Index(page, marker)
	if start < 0 {
		return nil, fmt.Errorf("Spotify public playlist page did not expose metadata")
	}
	start += len(marker)
	end := strings.Index(page[start:], "</script>")
	if end < 0 {
		return nil, fmt.Errorf("Spotify public playlist metadata is incomplete")
	}
	var data struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity struct {
							TrackList []struct {
								Title    string `json:"title"`
								Subtitle string `json:"subtitle"`
							} `json:"trackList"`
						} `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(page[start:start+end])), &data); err != nil {
		return nil, fmt.Errorf("decode Spotify public playlist metadata: %w", err)
	}
	items := data.Props.PageProps.State.Data.Entity.TrackList
	tracks := make([]Track, 0, min(len(items), maxPublicTracks))
	for _, item := range items {
		if item.Title == "" {
			continue
		}
		tracks = append(tracks, Track{Name: item.Title, Artist: item.Subtitle})
		if len(tracks) == maxPublicTracks {
			break
		}
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("Spotify public playlist page exposed no playable tracks; the playlist may be private or unsupported")
	}
	return tracks, nil
}

func parseSpotifyResource(input string) (resourceType, id string, ok bool) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "spotify:") {
		parts := strings.Split(input, ":")
		if len(parts) == 3 && parts[1] != "" && parts[2] != "" {
			return parts[1], parts[2], true
		}
		return "", "", false
	}
	parsed, err := url.Parse(input)
	if err != nil || (parsed.Hostname() != "open.spotify.com" && parsed.Hostname() != "www.open.spotify.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func IsSpotifyLink(input string) bool {
	_, _, ok := parseSpotifyResource(input)
	return ok
}

type apiTrack struct {
	Name    string `json:"name"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

type playlistPage struct {
	Next  string `json:"next"`
	Items []struct {
		Item  apiTrack `json:"item"`
		Track apiTrack `json:"track"`
	} `json:"items"`
}

func toTrack(track apiTrack) Track {
	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if artist.Name != "" {
			artists = append(artists, artist.Name)
		}
	}
	return Track{Name: track.Name, Artist: strings.Join(artists, ", ")}
}
