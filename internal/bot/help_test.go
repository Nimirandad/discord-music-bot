package bot

import (
	"strings"
	"testing"
)

func TestHelpMessageListsAvailableCommands(t *testing.T) {
	for _, command := range []string{
		"!play", "!join", "!pause", "!skip", "!previous", "!queue", "!shuffle",
		"!remove", "!move", "!skipto", "!clearqueue", "!stop", "!leave", "!help", "!ayuda",
	} {
		if !strings.Contains(helpMessage(), command) {
			t.Errorf("help message does not include %s", command)
		}
	}
}
