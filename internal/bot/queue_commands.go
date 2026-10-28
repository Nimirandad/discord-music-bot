package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/events"
)

func parseQueuePositions(command, args string) ([]int, error) {
	usage := map[string]string{
		"remove": "!remove <posición>", "move": "!move <origen> <destino>",
		"clearqueue": "!clearqueue", "skipto": "!skipto <posición>",
	}
	want := 1
	if command == "move" {
		want = 2
	} else if command == "clearqueue" {
		want = 0
	}
	fields := strings.Fields(args)
	if len(fields) != want {
		return nil, fmt.Errorf("Uso: `%s`. Las posiciones son las canciones en espera de `!queue`.", usage[command])
	}
	positions := make([]int, len(fields))
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || value < 1 {
			return nil, fmt.Errorf("Usa posiciones enteras desde 1, como aparecen en `!queue`.")
		}
		positions[i] = value
	}
	return positions, nil
}

func (b *Bot) manageQueueCommand(event *events.GuildMessageCreate, command, args string) {
	if command == "clearqueue" && !strings.EqualFold(strings.TrimSpace(args), "confirmar") {
		b.sendMessage(event, "Para vaciar la cola, confirma con `!clearqueue confirmar`. La canción actual seguirá sonando.")
		return
	}
	if command == "clearqueue" {
		args = ""
	}
	positions, err := parseQueuePositions(command, args)
	if err != nil {
		b.sendMessage(event, userFacingError(err))
		return
	}
	p := b.player(event.GuildID)
	if p == nil {
		b.sendMessage(event, "No hay canciones en espera.")
		return
	}
	var response string
	switch command {
	case "remove":
		track, removeErr := p.Remove(positions[0])
		err = removeErr
		response = fmt.Sprintf("Quité de la cola: %s.", displayMusicTitle(track))
	case "move":
		err = p.Move(positions[0], positions[1])
		response = fmt.Sprintf("Moví la canción de la posición %d a la %d.", positions[0], positions[1])
	case "clearqueue":
		count, clearErr := b.clearWaitingQueue(event.GuildID, p)
		err = clearErr
		response = fmt.Sprintf("Vacié la cola (%d canciones) y cancelé la carga pendiente de playlists. La canción actual continúa.", count)
	case "skipto":
		track, skipErr := p.SkipTo(positions[0])
		err = skipErr
		response = fmt.Sprintf("Saltando a la posición %d: %s.", positions[0], displayMusicTitle(track))
	}
	if err != nil {
		b.sendMessage(event, userFacingError(err))
		return
	}
	b.sendMessage(event, response)
}
