package bot

const helpMessageText = "**Comandos de MusicBot**\n" +
	"`!play <canción, URL o playlist>` / `/play consulta:<texto>` — busca o reproduce música.\n" +
	"`!join` / `/join` — conecta al canal de voz donde estás.\n" +
	"`!pause` / `/pause` — pausa o reanuda la canción.\n" +
	"`!skip` / `/skip` — salta a la siguiente canción.\n" +
	"`!previous` / `/previous` — vuelve a la canción anterior.\n" +
	"`!queue [página]` / `/queue pagina:<n>` — muestra diez canciones por página y la duración total.\n" +
	"`!shuffle` / `/shuffle` — mezcla las canciones en espera.\n" +
	"`!remove <posición>` / `/remove` — quita una canción de la cola.\n" +
	"`!move <origen> <destino>` / `/move` — cambia una canción de posición.\n" +
	"`!skipto <posición>` / `/skipto` — salta a una canción en espera.\n" +
	"`!clearqueue confirmar` / `/clearqueue` — vacía la cola tras confirmarlo.\n" +
	"`!stop` o `!leave` / `/stop` o `/leave` — detiene la música y desconecta.\n" +
	"`!help` o `!ayuda` / `/help` — muestra esta ayuda.\n\n" +
	"Las posiciones empiezan en 1. Los comandos de música se usan en el canal asignado; la ayuda está disponible en cualquier canal de texto."

func helpMessage() string {
	return helpMessageText
}
