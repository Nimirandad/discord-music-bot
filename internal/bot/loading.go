package bot

import (
	"context"
	"discord-music-bot/internal/player"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"time"
)

func loadingMusicMessage() discord.MessageCreate {
	return discord.NewMessageCreate().WithEmbeds(discord.NewEmbed().
		WithDescription("⏳ Recibí tu solicitud. Buscando y preparando la música…").WithColor(0x2B2D31))
}

func (b *Bot) startLoadingTyping(ctx context.Context, channelID snowflake.ID) context.CancelFunc {
	typingCtx, cancel := context.WithCancel(ctx)
	go runLoadingTyping(typingCtx, func() { _ = b.client.Rest.SendTyping(channelID) }, 7*time.Second)
	return cancel
}

func runLoadingTyping(ctx context.Context, send func(), interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		send()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func waitForPlaybackStart(ctx context.Context, snapshot func() player.Snapshot, resource string) {
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := snapshot()
		if state.Current != nil {
			if state.Current.URL != resource || !state.Loading {
				return
			}
		} else if len(state.Queue) == 0 {
			return
		}
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
		}
	}
}
