package bot

import "testing"

func TestQueueCommandPositionValidation(t *testing.T) {
	for _, tc := range []struct {
		command, args string
		valid         bool
	}{
		{"remove", "2", true}, {"move", "3 1", true}, {"skipto", "1", true}, {"clearqueue", "", true},
		{"remove", "0", false}, {"move", "1", false}, {"move", "-1 2", false}, {"skipto", "2.5", false}, {"clearqueue", "1", false}, {"remove", "1 2", false},
	} {
		_, err := parseQueuePositions(tc.command, tc.args)
		if (err == nil) != tc.valid {
			t.Errorf("%s %q: %v", tc.command, tc.args, err)
		}
		if !isMusicCommand(tc.command) {
			t.Errorf("%s bypasses assigned music channel", tc.command)
		}
	}
}
