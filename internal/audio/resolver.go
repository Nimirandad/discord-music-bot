package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	initialSearchCandidates = 1
	searchCandidates        = 5
)

type Track struct {
	Title           string
	Artist          string
	URL             string
	Duration        time.Duration
	Requester       string
	RequesterID     string
	Fallbacks       []Track
	FallbackQueries []string
}

type Resolver struct {
	YTDLPPath   string
	CookiesFile string
}

type ytDLPResult struct {
	Title          string        `json:"title"`
	Track          string        `json:"track"`
	ID             string        `json:"id"`
	URL            string        `json:"url"`
	Uploader       string        `json:"uploader"`
	Channel        string        `json:"channel"`
	Creator        string        `json:"creator"`
	Artist         string        `json:"artist"`
	WebpageURL     string        `json:"webpage_url"`
	Duration       float64       `json:"duration"`
	DurationString string        `json:"duration_string"`
	Entries        []ytDLPResult `json:"entries"`
}

func NewResolver() *Resolver {
	return &Resolver{YTDLPPath: "yt-dlp"}
}

func (r *Resolver) Resolve(ctx context.Context, query string) (Track, error) {
	query = normalizeQuery(query)
	if query == "" {
		return Track{}, fmt.Errorf("search query cannot be empty")
	}
	if looksLikeURL(query) {
		tracks, err := r.resolveTracks(ctx, query)
		if err != nil {
			return Track{}, err
		}
		if len(tracks) == 0 {
			return Track{}, fmt.Errorf("yt-dlp returned no playable tracks")
		}
		primary := tracks[0]
		search := strings.TrimSpace(primary.Title + " " + primary.Artist)
		if search == "" {
			return primary, nil
		}
		primary.FallbackQueries = fallbackSearchQueries(search)
		return primary, nil
	}

	queries := fallbackSearchQueries(query)
	for index, searchQuery := range queries {
		tracks, err := r.resolveSearch(ctx, searchQuery, initialSearchCandidates)
		if err != nil {
			return Track{}, err
		}
		if len(tracks) > 0 {
			track := tracks[0]
			track.FallbackQueries = append(append([]string(nil), queries[index+1:]...), searchQuery)
			return track, nil
		}
	}
	return Track{}, fmt.Errorf("yt-dlp returned no playable tracks for query %q after %d search attempts", query, len(queries))
}

func (r *Resolver) ResolveFallbackCandidates(ctx context.Context, query string) (Track, error) {
	query = normalizeQuery(query)
	if query == "" {
		return Track{}, fmt.Errorf("fallback search query cannot be empty")
	}
	tracks, err := r.resolveSearch(ctx, query, searchCandidates)
	if err != nil {
		return Track{}, err
	}
	if len(tracks) == 0 {
		return Track{}, fmt.Errorf("yt-dlp returned no fallback candidates for query %q", query)
	}
	return withFallbacks(tracks), nil
}

func (r *Resolver) ResolveAlternatives(ctx context.Context, queries ...string) (Track, error) {
	var lastErr error
	for index, query := range queries {
		if err := ctx.Err(); err != nil {
			return Track{}, err
		}
		track, err := r.Resolve(ctx, query)
		if err != nil {
			lastErr = err
			continue
		}
		remainingQueries := queries[index+1:]
		return withDeferredSearchQueries(track, remainingQueries), nil
	}
	if lastErr != nil {
		return Track{}, fmt.Errorf("all alternative searches failed: %w", lastErr)
	}
	return Track{}, fmt.Errorf("no alternative searches provided")
}

func withDeferredSearchQueries(track Track, queries []string) Track {
	if len(queries) == 0 {
		return track
	}
	if len(track.Fallbacks) == 0 {
		track.FallbackQueries = append(track.FallbackQueries, queries...)
	} else {
		lastFallback := len(track.Fallbacks) - 1
		track.Fallbacks[lastFallback].FallbackQueries = append(track.Fallbacks[lastFallback].FallbackQueries, queries...)
	}
	return track
}

func (r *Resolver) resolveSearch(ctx context.Context, query string, candidateCount int) ([]Track, error) {
	return r.resolveTracks(ctx, fmt.Sprintf("ytsearch%d:%s", candidateCount, query))
}

func fallbackSearchQueries(query string) []string {
	queries := []string{query}
	seen := map[string]struct{}{strings.ToLower(query): {}}
	parts := strings.FieldsFunc(query, func(character rune) bool {
		switch character {
		case '-', '–', '—', '|':
			return true
		default:
			return false
		}
	})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := strings.ToLower(part)
		if part == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		queries = append(queries, part)
	}
	return queries
}

func normalizeQuery(query string) string {
	query = strings.TrimSpace(query)
	for {
		switch {
		case strings.HasPrefix(strings.ToLower(query), "query:"):
			query = strings.TrimSpace(query[len("query:"):])
		default:
			lowerQuery := strings.ToLower(query)
			if strings.HasPrefix(lowerQuery, "ytsearch") {
				colon := strings.IndexByte(lowerQuery, ':')
				if colon > len("ytsearch") {
					if _, err := strconv.Atoi(lowerQuery[len("ytsearch"):colon]); err == nil {
						query = strings.TrimSpace(query[colon+1:])
						continue
					}
				}
			}
			return query
		}
	}
}

func (r *Resolver) ResolveRelated(ctx context.Context, previous Track) (Track, error) {
	query := strings.TrimSpace("songs similar to " + previous.Title + " " + previous.Artist)
	tracks, err := r.resolveTracks(ctx, "ytsearch5:"+query)
	if err != nil {
		return Track{}, err
	}
	related := make([]Track, 0, len(tracks))
	for _, track := range tracks {
		if track.URL == previous.URL || strings.EqualFold(track.Title, previous.Title) {
			continue
		}
		related = append(related, track)
	}
	if len(related) > 0 {
		return withFallbacks(related), nil
	}
	return Track{}, fmt.Errorf("yt-dlp found no related track distinct from %q", previous.Title)
}

func withFallbacks(tracks []Track) Track {
	primary := tracks[0]
	primary.Fallbacks = append([]Track(nil), tracks[1:]...)
	return primary
}

func appendUniqueTracks(existing, candidates []Track, excludedURL string) []Track {
	seen := make(map[string]struct{}, len(existing)+len(candidates)+1)
	seen[excludedURL] = struct{}{}
	for _, track := range existing {
		seen[track.URL] = struct{}{}
	}
	for _, track := range candidates {
		if track.URL == "" {
			continue
		}
		if _, exists := seen[track.URL]; exists {
			continue
		}
		seen[track.URL] = struct{}{}
		existing = append(existing, track)
	}
	return existing
}

func (r *Resolver) resolveTracks(ctx context.Context, query string) ([]Track, error) {
	args := []string{"--dump-single-json", "--no-warnings", "--no-cache-dir"}
	if isYTDLPSearch(query) {
		args = append(args, "--flat-playlist")
	} else {
		args = append(args, "--no-playlist")
	}
	args = appendYTDLPCookies(args, r.CookiesFile)
	args = appendYTDLPPOProvider(args)
	args = append(args, query)
	command := exec.CommandContext(ctx, r.YTDLPPath, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("resolve media with yt-dlp: %w: %s", err, message)
		}
		return nil, fmt.Errorf("resolve media with yt-dlp: %w", err)
	}

	var result ytDLPResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("decode yt-dlp metadata: %w", err)
	}
	results := result.Entries
	if len(results) == 0 {
		results = []ytDLPResult{result}
	}
	tracks := make([]Track, 0, len(results))
	for _, result := range results {
		if track, ok := trackFromResult(result); ok {
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}

func appendYTDLPCookies(args []string, cookiesFile string) []string {
	if strings.TrimSpace(cookiesFile) == "" {
		return args
	}
	return append(args, "--cookies", cookiesFile)
}

func appendYTDLPPOProvider(args []string) []string {
	return append(args,
		"--extractor-args",
		"youtube:player_client=mweb",
		"--extractor-args",
		"youtubepot-bgutilhttp:base_url=http://bgutil-pot-provider:4416",
	)
}

func isYTDLPSearch(query string) bool {
	lowerQuery := strings.ToLower(query)
	if !strings.HasPrefix(lowerQuery, "ytsearch") {
		return false
	}
	colon := strings.IndexByte(lowerQuery, ':')
	if colon < len("ytsearch") {
		return false
	}
	_, err := strconv.Atoi(lowerQuery[len("ytsearch"):colon])
	return err == nil
}

func trackFromResult(result ytDLPResult) (Track, bool) {
	title := firstNonEmpty(result.Title, result.Track)
	url := firstNonEmpty(result.WebpageURL, result.URL)
	if url == "" && result.ID != "" {
		url = "https://www.youtube.com/watch?v=" + result.ID
	}
	if title == "" || url == "" {
		return Track{}, false
	}
	duration := time.Duration(result.Duration * float64(time.Second))
	if duration <= 0 {
		duration = parseDuration(result.DurationString)
	}
	return Track{
		Title:    title,
		Artist:   firstNonEmpty(result.Artist, result.Creator, result.Channel, result.Uploader),
		URL:      url,
		Duration: duration,
	}, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseDuration(value string) time.Duration {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0
	}
	var seconds float64
	for _, part := range parts {
		component, err := strconv.ParseFloat(part, 64)
		if err != nil || component < 0 {
			return 0
		}
		seconds = seconds*60 + component
	}
	return time.Duration(seconds * float64(time.Second))
}

func looksLikeURL(value string) bool {
	return strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")
}
