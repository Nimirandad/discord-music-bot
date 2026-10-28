package bot

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

func TestSlashCommandsExposeCurrentMusicControls(t *testing.T) {
	want := map[string]bool{
		"help": false, "play": false, "join": false, "pause": false, "skip": false,
		"previous": false, "shuffle": false, "queue": false, "remove": false,
		"move": false, "skipto": false, "clearqueue": false, "stop": false, "leave": false,
	}
	for _, command := range slashCommands() {
		slash, ok := command.(discord.SlashCommandCreate)
		if !ok {
			t.Fatalf("slashCommands() returned unexpected command type %T", command)
		}
		if _, exists := want[slash.Name]; !exists {
			t.Errorf("unexpected slash command %q", slash.Name)
		}
		want[slash.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("slash command %q was not registered", name)
		}
	}
}
