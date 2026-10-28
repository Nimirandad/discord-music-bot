package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUserFacingErrorProvidesActionableGuidance(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: "tardó demasiado"},
		{name: "Spotify forbidden", err: errors.New("Spotify API returned HTTP 403"), want: "Spotify rechazó"},
		{name: "no playable audio", err: errors.New("yt-dlp returned no playable tracks"), want: "No encontré audio reproducible"},
		{name: "technical fallback", err: errors.New("internal decode failure"), want: "revisa los logs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := userFacingError(test.err); !strings.Contains(got, test.want) {
				t.Errorf("userFacingError() = %q, want it to contain %q", got, test.want)
			}
		})
	}
}
