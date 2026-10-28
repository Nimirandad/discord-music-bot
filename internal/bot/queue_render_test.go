package bot

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"discord-music-bot/internal/audio"
	"discord-music-bot/internal/player"
)

func TestParseQueuePage(t *testing.T) {
	for _, test := range []struct {
		args string
		want int
		bad  bool
	}{
		{args: "", want: 1},
		{args: "2", want: 2},
		{args: "0", bad: true},
		{args: "first", bad: true},
		{args: "2 3", bad: true},
	} {
		got, err := parseQueuePage(test.args)
		if test.bad && err == nil {
			t.Errorf("parseQueuePage(%q) should fail", test.args)
		}
		if !test.bad && (err != nil || got != test.want) {
			t.Errorf("parseQueuePage(%q) = %d, %v; want %d", test.args, got, err, test.want)
		}
	}
}

func TestRenderQueuePaginatesAndShowsTotalDuration(t *testing.T) {
	queue := make([]audio.Track, 12)
	for i := range queue {
		queue[i] = audio.Track{Title: "Track " + strconv.Itoa(i+1), Duration: time.Minute}
	}
	snapshot := player.Snapshot{Current: &audio.Track{Title: "Current", Duration: 30 * time.Second}, Queue: queue}

	first, err := renderQueue(snapshot, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderQueue(snapshot, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Current", "Track 1", "Track 10", "Página 1/2", "12:30"} {
		if !strings.Contains(first, want) {
			t.Errorf("first queue page missing %q: %s", want, first)
		}
	}
	if strings.Contains(first, "Track 11") || !strings.Contains(second, "Track 11") || !strings.Contains(second, "Track 12") {
		t.Fatalf("queue pages did not split waiting tracks correctly: page1=%q page2=%q", first, second)
	}
	if _, err := renderQueue(snapshot, 3); err == nil {
		t.Fatal("renderQueue() accepted a page beyond the queue")
	}
}

func TestFormatQueueDuration(t *testing.T) {
	if got := formatQueueDuration(0); got != "desconocida" {
		t.Fatalf("formatQueueDuration(0) = %q", got)
	}
	if got := formatQueueDuration(61 * time.Minute); got != "1:01:00" {
		t.Fatalf("formatQueueDuration() = %q, want 1:01:00", got)
	}
}
