package audio

import (
	"testing"
	"time"
)

func TestTrackFromResultUsesResolvedYouTubeMetadata(t *testing.T) {
	track, ok := trackFromResult(ytDLPResult{
		Title:      "Acepta tu peo | Media Semana",
		Uploader:   "Dantesco Media",
		Channel:    "Dantesco Media",
		WebpageURL: "https://www.youtube.com/watch?v=E1-aPsAZdCI",
		Duration:   7670,
	})
	if !ok {
		t.Fatal("trackFromResult() did not return a playable track")
	}
	if track.Title != "Acepta tu peo | Media Semana" {
		t.Errorf("track title = %q", track.Title)
	}
	if track.Artist != "Dantesco Media" {
		t.Errorf("track artist = %q, want Dantesco Media", track.Artist)
	}
	if track.Duration != 2*time.Hour+7*time.Minute+50*time.Second {
		t.Errorf("track duration = %v, want 2h7m50s", track.Duration)
	}
}

func TestTrackFromResultUsesFallbackMetadata(t *testing.T) {
	track, ok := trackFromResult(ytDLPResult{
		Track:          "Fallback title",
		Creator:        "Fallback artist",
		WebpageURL:     "https://example.test/track",
		DurationString: "3:24",
	})
	if !ok {
		t.Fatal("trackFromResult() did not return a track with fallback metadata")
	}
	if track.Title != "Fallback title" || track.Artist != "Fallback artist" || track.Duration != 3*time.Minute+24*time.Second {
		t.Errorf("track = %+v, want fallback title, artist and 3m24s duration", track)
	}
}

func TestTrackFromResultBuildsYouTubeURLFromFlatSearchResult(t *testing.T) {
	track, ok := trackFromResult(ytDLPResult{
		ID:    "SC4xMk98Pdc",
		Title: "Post Malone - Congratulations (Official Music Video) ft. Quavo",
	})
	if !ok {
		t.Fatal("trackFromResult() did not return a playable track from flat search metadata")
	}
	if track.URL != "https://www.youtube.com/watch?v=SC4xMk98Pdc" {
		t.Errorf("track URL = %q, want YouTube watch URL", track.URL)
	}
}

func TestTrackFromResultPrefersResolvedAndProvidedURLs(t *testing.T) {
	for _, test := range []struct {
		name   string
		result ytDLPResult
		want   string
	}{
		{
			name:   "provided result URL",
			result: ytDLPResult{Title: "Song", URL: "https://www.youtube.com/watch?v=from-url", ID: "from-id"},
			want:   "https://www.youtube.com/watch?v=from-url",
		},
		{
			name:   "resolved webpage URL",
			result: ytDLPResult{Title: "Song", WebpageURL: "https://youtu.be/from-webpage", URL: "https://youtube.com/watch?v=from-url"},
			want:   "https://youtu.be/from-webpage",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			track, ok := trackFromResult(test.result)
			if !ok {
				t.Fatal("trackFromResult() did not return a playable track")
			}
			if track.URL != test.want {
				t.Errorf("track URL = %q, want %q", track.URL, test.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"3:24", 3*time.Minute + 24*time.Second},
		{"3:24.5", 3*time.Minute + 24*time.Second + 500*time.Millisecond},
		{"2:07:50", 2*time.Hour + 7*time.Minute + 50*time.Second},
		{"invalid", 0},
	} {
		if got := parseDuration(test.value); got != test.want {
			t.Errorf("parseDuration(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestNormalizeQueryUnwrapsYTDLPQueryPrefixes(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"query:https://www.youtube.com/watch?v=abc", "https://www.youtube.com/watch?v=abc"},
		{"ytsearch1:query:https://www.youtube.com/watch?v=abc", "https://www.youtube.com/watch?v=abc"},
		{"ytsearch5:Song title", "Song title"},
		{"  query:  Song title  ", "Song title"},
	} {
		if got := normalizeQuery(test.input); got != test.want {
			t.Errorf("normalizeQuery(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestWithFallbacksKeepsRemainingCandidatesInOrder(t *testing.T) {
	tracks := []Track{
		{Title: "first", URL: "https://youtube.com/watch?v=first"},
		{Title: "second", URL: "https://youtube.com/watch?v=second"},
		{Title: "third", URL: "https://youtube.com/watch?v=third"},
	}

	got := withFallbacks(tracks)
	if got.Title != "first" {
		t.Fatalf("primary track = %q, want first", got.Title)
	}
	if len(got.Fallbacks) != 2 {
		t.Fatalf("fallback count = %d, want 2", len(got.Fallbacks))
	}
	if got.Fallbacks[0].Title != "second" || got.Fallbacks[1].Title != "third" {
		t.Errorf("fallback order = %q, %q; want second, third", got.Fallbacks[0].Title, got.Fallbacks[1].Title)
	}
}

func TestFallbackSearchQueriesUsesDistinctTitleAndArtistParts(t *testing.T) {
	got := fallbackSearchQueries("花になって - Be a flower | Ryokuoushoku Shakai")
	want := []string{
		"花になって - Be a flower | Ryokuoushoku Shakai",
		"花になって",
		"Be a flower",
		"Ryokuoushoku Shakai",
	}
	if len(got) != len(want) {
		t.Fatalf("fallbackSearchQueries() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("fallbackSearchQueries()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestAppendUniqueTracksExcludesPrimaryAndDuplicateCandidates(t *testing.T) {
	got := appendUniqueTracks(nil, []Track{
		{Title: "primary", URL: "https://youtube.com/watch?v=primary"},
		{Title: "first fallback", URL: "https://youtube.com/watch?v=fallback"},
		{Title: "duplicate fallback", URL: "https://youtube.com/watch?v=fallback"},
	}, "https://youtube.com/watch?v=primary")
	if len(got) != 1 || got[0].Title != "first fallback" {
		t.Fatalf("appendUniqueTracks() = %+v, want only first fallback", got)
	}
}

func TestAppendUniqueTracksKeepsCandidatesAcrossAlternativeSearches(t *testing.T) {
	existing := []Track{{Title: "first", URL: "https://youtube.com/watch?v=first"}}
	got := appendUniqueTracks(existing, []Track{
		{Title: "duplicate", URL: "https://youtube.com/watch?v=first"},
		{Title: "second", URL: "https://youtube.com/watch?v=second"},
	}, "")
	if len(got) != 2 || got[0].Title != "first" || got[1].Title != "second" {
		t.Fatalf("appendUniqueTracks() = %+v, want first and second once", got)
	}

}

func TestWithDeferredSearchQueriesRunsAfterInitialCandidates(t *testing.T) {
	track := withFallbacks([]Track{
		{Title: "first", URL: "https://youtube.com/watch?v=first"},
		{Title: "second", URL: "https://youtube.com/watch?v=second"},
	})
	track = withDeferredSearchQueries(track, []string{"track artist", "track"})
	if len(track.FallbackQueries) != 0 {
		t.Fatalf("primary fallback queries = %v, want none", track.FallbackQueries)
	}
	if got := track.Fallbacks[0].FallbackQueries; len(got) != 2 || got[0] != "track artist" || got[1] != "track" {
		t.Fatalf("last candidate fallback queries = %v, want [track artist track]", got)
	}
}

func TestWithDeferredSearchQueriesPreservesExistingQueries(t *testing.T) {
	track := Track{Title: "first", FallbackQueries: []string{"same query"}}
	track = withDeferredSearchQueries(track, []string{"alternate query"})
	if len(track.FallbackQueries) != 2 || track.FallbackQueries[0] != "same query" || track.FallbackQueries[1] != "alternate query" {
		t.Fatalf("fallback queries = %v, want [same query alternate query]", track.FallbackQueries)
	}
}

func TestIsYTDLPSearch(t *testing.T) {
	for _, test := range []struct {
		query string
		want  bool
	}{
		{"ytsearch5:flower", true},
		{"YTSEARCH1:flower", true},
		{"https://www.youtube.com/watch?v=abc", false},
		{"ytsearch:flower", false},
		{"notytsearch5:flower", false},
	} {
		if got := isYTDLPSearch(test.query); got != test.want {
			t.Errorf("isYTDLPSearch(%q) = %t, want %t", test.query, got, test.want)
		}
	}
}

func TestAppendYTDLPCookies(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		cookies string
		want    []string
	}{
		{
			name:    "configured cookies file",
			args:    []string{"--no-warnings"},
			cookies: "/run/secrets/youtube-cookies.txt",
			want:    []string{"--no-warnings", "--cookies", "/run/secrets/youtube-cookies.txt"},
		},
		{
			name:    "blank cookies file",
			args:    []string{"--no-warnings"},
			cookies: "  ",
			want:    []string{"--no-warnings"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := appendYTDLPCookies(test.args, test.cookies)
			if len(got) != len(test.want) {
				t.Fatalf("appendYTDLPCookies() = %v, want %v", got, test.want)
			}
			for index := range test.want {
				if got[index] != test.want[index] {
					t.Errorf("appendYTDLPCookies()[%d] = %q, want %q", index, got[index], test.want[index])
				}
			}
		})
	}
}

func TestAppendYTDLPPOProvider(t *testing.T) {
	got := appendYTDLPPOProvider([]string{"--no-warnings"})
	want := []string{
		"--no-warnings",
		"--extractor-args",
		"youtube:player_client=mweb",
		"--extractor-args",
		"youtubepot-bgutilhttp:base_url=http://bgutil-pot-provider:4416",
	}
	if len(got) != len(want) {
		t.Fatalf("appendYTDLPPOProvider() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("appendYTDLPPOProvider()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
