package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DiscordToken        string
	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string
	SpotifyTokenFile    string
	YTDLPCookiesFile    string
	HTTPAddr            string
	AutoLeaveSeconds    int
}

func Load() (Config, error) {
	cfg := Config{
		DiscordToken:        strings.TrimSpace(os.Getenv("DISCORD_TOKEN")),
		SpotifyClientID:     strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_ID")),
		SpotifyClientSecret: strings.TrimSpace(os.Getenv("SPOTIFY_CLIENT_SECRET")),
		SpotifyRedirectURI:  strings.TrimSpace(os.Getenv("SPOTIFY_REDIRECT_URI")),
		SpotifyTokenFile:    strings.TrimSpace(os.Getenv("SPOTIFY_TOKEN_FILE")),
		YTDLPCookiesFile:    strings.TrimSpace(os.Getenv("YTDLP_COOKIES_FILE")),
		HTTPAddr:            strings.TrimSpace(os.Getenv("HTTP_ADDR")),
		AutoLeaveSeconds:    180,
	}
	if cfg.DiscordToken == "" {
		return Config{}, fmt.Errorf("DISCORD_TOKEN is required")
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	if raw := strings.TrimSpace(os.Getenv("AUTO_LEAVE_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 {
			return Config{}, fmt.Errorf("AUTO_LEAVE_SECONDS must be a positive integer")
		}
		cfg.AutoLeaveSeconds = seconds
	}
	if (cfg.SpotifyClientID == "") != (cfg.SpotifyClientSecret == "") {
		return Config{}, fmt.Errorf("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET must be set together")
	}
	if cfg.SpotifyClientID != "" {
		if cfg.SpotifyRedirectURI == "" {
			cfg.SpotifyRedirectURI = "http://127.0.0.1:18080/spotify/callback"
		}
		if cfg.SpotifyTokenFile == "" {
			cfg.SpotifyTokenFile = "/var/lib/discord-music-bot/spotify-token.json"
		}
	}
	return cfg, nil
}
