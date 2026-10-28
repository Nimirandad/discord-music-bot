package bot

const helpMessageText = "**Comandos de MusicBot**\n" +
	"`!play <canción, URL o playlist>` — busca o reproduce música.\n" +
	"`!join` — conecta al canal de voz donde estás.\n" +
	"`!pause` — pausa o reanuda la canción.\n" +
	"`!skip` — salta a la siguiente canción.\n" +
	"`!previous` — vuelve a la canción anterior.\n" +
	"`!queue` — muestra la canción actual y la cola.\n" +
	"`!shuffle` — mezcla las canciones en espera.\n" +
	"`!remove <posición>` — quita una canción de la cola.\n" +
	"`!move <origen> <destino>` — mueve una canción en la cola.\n" +
	"`!skipto <posición>` — salta a una canción de la cola.\n" +
	"`!clearqueue` — vacía la cola de espera.\n" +
	"`!stop` o `!leave` — detiene la música y desconecta.\n" +
	"`!help` o `!ayuda` — muestra esta ayuda.\n\n" +
	"Las posiciones empiezan en 1. Los comandos de música se usan en el canal asignado; `!help` está disponible en cualquier canal de texto."

func helpMessage() string {
	return helpMessageText
}
