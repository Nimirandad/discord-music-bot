package config

import "testing"

func TestLoadDefaultsSpotifyUserOAuthSettings(t *testing.T) {
	t.Setenv("DISCORD_TOKEN", "test-token")
	t.Setenv("SPOTIFY_CLIENT_ID", "client-id")
	t.Setenv("SPOTIFY_CLIENT_SECRET", "client-secret")
	t.Setenv("SPOTIFY_REDIRECT_URI", "")
	t.Setenv("SPOTIFY_TOKEN_FILE", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("AUTO_LEAVE_SECONDS", "")
	t.Setenv("YTDLP_COOKIES_FILE", "/run/secrets/youtube-cookies.txt")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SpotifyRedirectURI != "http://127.0.0.1:18080/spotify/callback" {
		t.Errorf("SpotifyRedirectURI = %q, want loopback callback default", cfg.SpotifyRedirectURI)
	}
	if cfg.SpotifyTokenFile != "/var/lib/discord-music-bot/spotify-token.json" {
		t.Errorf("SpotifyTokenFile = %q, want persistent token path default", cfg.SpotifyTokenFile)
	}
	if cfg.YTDLPCookiesFile != "/run/secrets/youtube-cookies.txt" {
		t.Errorf("YTDLPCookiesFile = %q, want configured cookies file path", cfg.YTDLPCookiesFile)
	}
}
