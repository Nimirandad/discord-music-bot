package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discord-music-bot/internal/player"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func slashCommands() []discord.ApplicationCommandCreate {
	queryOption := discord.ApplicationCommandOptionString{Name: "consulta", Description: "Nombre, enlace de canción o playlist", Required: true}
	positionOption := func(name, description string) discord.ApplicationCommandOption {
		minimum := 1
		return discord.ApplicationCommandOptionInt{Name: name, Description: description, Required: true, MinValue: &minimum}
	}
	return []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{Name: "help", Description: "Muestra los comandos disponibles"},
		discord.SlashCommandCreate{Name: "play", Description: "Busca o reproduce música", Options: []discord.ApplicationCommandOption{queryOption}},
		discord.SlashCommandCreate{Name: "join", Description: "Conecta al canal de voz donde estás"},
		discord.SlashCommandCreate{Name: "pause", Description: "Pausa o reanuda la canción"},
		discord.SlashCommandCreate{Name: "skip", Description: "Salta a la siguiente canción"},
		discord.SlashCommandCreate{Name: "previous", Description: "Vuelve a la canción anterior"},
		discord.SlashCommandCreate{Name: "shuffle", Description: "Mezcla las canciones en espera"},
		discord.SlashCommandCreate{Name: "queue", Description: "Muestra una página de la cola y su duración", Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionInt{Name: "pagina", Description: "Número de página (10 canciones)", Required: false, MinValue: intPointer(1)},
		}},
		discord.SlashCommandCreate{Name: "remove", Description: "Quita una canción de la cola", Options: []discord.ApplicationCommandOption{positionOption("posicion", "Posición en espera")}},
		discord.SlashCommandCreate{Name: "move", Description: "Mueve una canción en la cola", Options: []discord.ApplicationCommandOption{
			positionOption("origen", "Posición actual en espera"), positionOption("destino", "Nueva posición en espera"),
		}},
		discord.SlashCommandCreate{Name: "skipto", Description: "Salta a una posición de la cola", Options: []discord.ApplicationCommandOption{positionOption("posicion", "Posición de destino en espera")}},
		discord.SlashCommandCreate{Name: "clearqueue", Description: "Vacía la cola después de confirmarlo", Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionBool{Name: "confirmar", Description: "Confirma que quieres vaciar las canciones en espera", Required: true},
		}},
		discord.SlashCommandCreate{Name: "stop", Description: "Detiene la música y desconecta"},
		discord.SlashCommandCreate{Name: "leave", Description: "Detiene la música y desconecta"},
	}
}

func intPointer(value int) *int { return &value }

func commandChannelMatches(assignedChannelID, incomingChannelID snowflake.ID) bool {
	return assignedChannelID == 0 || assignedChannelID == incomingChannelID
}

func (b *Bot) onApplicationCommand(event *events.ApplicationCommandInteractionCreate) {
	data := event.SlashCommandInteractionData()
	if data.CommandName() == "help" {
		b.respondSlash(event, helpMessage(), true)
		return
	}
	guildID := event.GuildID()
	if guildID == nil {
		b.respondSlash(event, "Los comandos de música solo funcionan dentro de un servidor.", true)
		return
	}
	channelID := event.Channel().ID()
	if !b.slashCommandChannelAllowed(*guildID, channelID) {
		b.mu.Lock()
		commandChannelID := b.commandChannels[*guildID]
		b.mu.Unlock()
		b.respondSlash(event, fmt.Sprintf("Este servidor usa el canal de música <#%s>.", commandChannelID.String()), true)
		return
	}

	switch data.CommandName() {
	case "play":
		b.slashPlay(event, *guildID, channelID, data.String("consulta"))
	case "join":
		b.slashJoin(event, *guildID)
	case "queue":
		page := data.Int("pagina")
		if page < 1 {
			page = 1
		}
		p := b.player(*guildID)
		if p == nil {
			b.respondSlash(event, "La cola está vacía.", true)
			return
		}
		content, err := renderQueue(p.Snapshot(), page)
		if err != nil {
			b.respondSlash(event, err.Error(), true)
			return
		}
		b.respondSlash(event, content, true)
	case "pause":
		p := b.player(*guildID)
		if p == nil {
			b.respondSlash(event, "No hay ninguna canción en reproducción.", true)
			return
		}
		paused, err := p.TogglePause()
		if err != nil {
			b.respondSlash(event, userFacingError(err), true)
			return
		}
		if paused {
			b.respondSlash(event, "Reproducción pausada.", true)
		} else {
			b.respondSlash(event, "Reproducción reanudada.", true)
		}
	case "skip", "previous":
		p := b.player(*guildID)
		if p == nil {
			b.respondSlash(event, "No hay ninguna canción en reproducción.", true)
			return
		}
		var err error
		response := "Canción saltada."
		if data.CommandName() == "previous" {
			err = p.Previous()
			response = "Reproduciendo la canción anterior."
		} else {
			err = p.Skip()
		}
		if err != nil {
			b.respondSlash(event, userFacingError(err), true)
			return
		}
		b.respondSlash(event, response, true)
	case "shuffle":
		p := b.player(*guildID)
		if p == nil || p.ShuffleQueue() == 0 {
			b.respondSlash(event, "Agrega al menos dos canciones en espera para mezclarlas.", true)
			return
		}
		b.respondSlash(event, "Mezclé las canciones que estaban en espera.", true)
	case "remove", "move", "skipto":
		b.slashManageQueue(event, *guildID, data)
	case "clearqueue":
		if !data.Bool("confirmar") {
			b.respondSlash(event, "No vacié la cola. Vuelve a ejecutar `/clearqueue` y activa `confirmar`.", true)
			return
		}
		p := b.player(*guildID)
		if p == nil {
			b.respondSlash(event, "No hay canciones en espera.", true)
			return
		}
		count, err := b.clearWaitingQueue(*guildID, p)
		if err != nil {
			b.respondSlash(event, userFacingError(err), true)
			return
		}
		b.respondSlash(event, fmt.Sprintf("Vacié la cola (%d canciones). La canción actual continúa.", count), true)
	case "stop", "leave":
		b.stopGuildWithActor(*guildID, event.User().ID)
		b.respondSlash(event, "Música detenida y bot desconectado.", true)
	default:
		b.respondSlash(event, "No reconozco ese comando. Usa `/help` para ver la lista.", true)
	}
}

func (b *Bot) slashCommandChannelAllowed(guildID, channelID snowflake.ID) bool {
	b.mu.Lock()
	assigned := b.commandChannels[guildID]
	b.mu.Unlock()
	return commandChannelMatches(assigned, channelID)
}

func (b *Bot) respondSlash(event *events.ApplicationCommandInteractionCreate, content string, ephemeral bool) {
	if err := event.CreateMessage(discord.NewMessageCreate().WithContent(content).WithEphemeral(ephemeral)); err != nil {
		b.logger.Error("respond to slash command", "command", event.SlashCommandInteractionData().CommandName(), "error", err)
	}
}

func (b *Bot) editSlashResponse(event *events.ApplicationCommandInteractionCreate, content string) {
	if _, err := event.Client().Rest.UpdateInteractionResponse(event.ApplicationID(), event.Token(), discord.NewMessageUpdate().WithContent(content)); err != nil {
		b.logger.Error("update slash command response", "command", event.SlashCommandInteractionData().CommandName(), "error", err)
	}
}

func (b *Bot) slashPlay(event *events.ApplicationCommandInteractionCreate, guildID, channelID snowflake.ID, query string) {
	if strings.TrimSpace(query) == "" {
		b.respondSlash(event, "Indica una canción, enlace o playlist en `consulta`.", true)
		return
	}
	user := event.User()
	voiceState, ok := event.Client().Caches.VoiceState(guildID, user.ID)
	if !ok || voiceState.ChannelID == nil {
		b.respondSlash(event, "Entra a un canal de voz antes de reproducir música.", true)
		return
	}
	b.mu.Lock()
	if b.commandChannels[guildID] == 0 {
		b.commandChannels[guildID] = channelID
	}
	b.mu.Unlock()
	if err := event.DeferCreateMessage(true); err != nil {
		b.logger.Error("defer slash play response", "error", err)
		return
	}
	go func() {
		err := b.resolveAndEnqueue(guildID, *voiceState.ChannelID, channelID, user.Username, user.ID.String(), query)
		if err != nil {
			b.editSlashResponse(event, "No pude reproducirlo: "+userFacingError(err))
			return
		}
		b.editSlashResponse(event, "Listo. La solicitud se agregó a la cola.")
	}()
}

func (b *Bot) slashJoin(event *events.ApplicationCommandInteractionCreate, guildID snowflake.ID) {
	user := event.User()
	voiceState, ok := event.Client().Caches.VoiceState(guildID, user.ID)
	if !ok || voiceState.ChannelID == nil {
		b.respondSlash(event, "Entra a un canal de voz antes de usar `/join`.", true)
		return
	}
	if err := event.DeferCreateMessage(true); err != nil {
		b.logger.Error("defer slash join response", "error", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := b.joinVoice(ctx, guildID, *voiceState.ChannelID); err != nil {
			b.editSlashResponse(event, "No pude conectarme al canal de voz. Comprueba mis permisos. "+userFacingError(err))
			return
		}
		b.cancelLeaveTimer(guildID)
		b.editSlashResponse(event, "Me conecté a tu canal de voz.")
	}()
}

func (b *Bot) slashManageQueue(event *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, data discord.SlashCommandInteractionData) {
	p := b.player(guildID)
	if p == nil {
		b.respondSlash(event, "No hay canciones en espera.", true)
		return
	}
	var response string
	var err error
	switch data.CommandName() {
	case "remove":
		track, removeErr := p.Remove(data.Int("posicion"))
		err = removeErr
		response = "Quité de la cola: " + displayMusicTitle(track) + "."
	case "move":
		from, to := data.Int("origen"), data.Int("destino")
		err = p.Move(from, to)
		response = fmt.Sprintf("Moví la canción de la posición %d a la %d.", from, to)
	case "skipto":
		track, skipErr := p.SkipTo(data.Int("posicion"))
		err = skipErr
		response = "Saltando a la posición indicada: " + displayMusicTitle(track) + "."
	}
	if err != nil {
		b.respondSlash(event, userFacingError(err), true)
		return
	}
	b.respondSlash(event, response, true)
}

func (b *Bot) clearWaitingQueue(guildID snowflake.ID, p *player.Player) (int, error) {
	b.mu.Lock()
	for _, cancel := range b.playlistLoads[guildID] {
		cancel()
	}
	delete(b.playlistLoads, guildID)
	b.mu.Unlock()
	return p.ClearQueue()
}
