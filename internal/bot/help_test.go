package bot

import (
	"strings"
	"testing"
)

func TestHelpMessageListsAvailableSlashCommands(t *testing.T) {
	for _, command := range []string{"/play", "/join", "/pause", "/skip", "/previous", "/queue", "/shuffle", "/remove", "/move", "/skipto", "/clearqueue", "/stop", "/leave", "/help"} {
		if !strings.Contains(helpMessage(), command) {
			t.Errorf("help message does not include %s", command)
		}
	}
	if strings.Contains(helpMessage(), "!") {
		t.Error("help message should no longer advertise prefix commands")
	}
}
