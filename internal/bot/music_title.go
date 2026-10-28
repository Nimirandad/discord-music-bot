package bot

import (
	"discord-music-bot/internal/audio"
	"regexp"
	"strings"
)

var musicBrackets = regexp.MustCompile(`\([^()]*\)|\[[^\[\]]*\]`)
var musicDecoration = regexp.MustCompile(`(?i)\b(?:official\s+(?:(?:music|lyric|lyrics)\s+)?video|official\s+(?:audio|lyric(?:s)?(?:\s+video)?|visuali[sz]er)|music\s+video|lyric(?:s)?(?:\s+video)?|audio\s+oficial|v[ií]deo\s+oficial|visuali[sz]er|letras?|audio|hd|4k)\b`)
var musicSuffix = regexp.MustCompile(`(?i)(?:\s*[-–—|:]\s*|\s+)(?:official\s+(?:(?:music|lyric|lyrics)\s+)?video|official\s+(?:audio|lyric(?:s)?(?:\s+video)?|visuali[sz]er)|lyric(?:s)?(?:\s+video)?|music\s+video|visuali[sz]er|letras?|v[ií]deo\s+oficial)(?:\s+(?:hd|4k))?\s*$`)
var artistChannelSuffix = regexp.MustCompile(`(?i)(?:\s*-\s*Topic|\s*VEVO)$`)

func cleanMusicArtist(value string) string {
	cleaned := strings.TrimSpace(artistChannelSuffix.ReplaceAllString(strings.TrimSpace(value), ""))
	if cleaned == "" {
		return strings.TrimSpace(value)
	}
	return cleaned
}

func displayMusicTitle(track audio.Track) string {
	if track.SongTitle != "" {
		return strings.TrimSpace(track.SongTitle)
	}
	original := strings.TrimSpace(track.Title)
	cleaned := musicBrackets.ReplaceAllStringFunc(original, func(group string) string {
		inner := group[1 : len(group)-1]
		if strings.Trim(musicDecoration.ReplaceAllString(inner, ""), " \t-/|,:;") == "" {
			return ""
		}
		return group
	})
	for {
		next := musicSuffix.ReplaceAllString(cleaned, "")
		if next == cleaned {
			break
		}
		cleaned = next
	}
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	cleaned = strings.Trim(cleaned, " -–—|:")
	artist := cleanMusicArtist(track.Artist)
	for _, separator := range []string{" - ", " – ", " — "} {
		prefix := artist + separator
		if artist != "" && len(cleaned) > len(prefix) && strings.EqualFold(cleaned[:len(prefix)], prefix) {
			cleaned = strings.TrimSpace(cleaned[len(prefix):])
			break
		}
	}
	if cleaned == "" {
		return original
	}
	return cleaned
}
