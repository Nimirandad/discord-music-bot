package bot

const helpMessageText = "**Comandos de MusicBot**\n" +
	"`/play consulta:<canción, URL o playlist>` — busca o reproduce música.\n" +
	"`/join` — conecta al canal de voz donde estás.\n" +
	"`/pause` — pausa o reanuda la canción.\n" +
	"`/skip` — salta a la siguiente canción.\n" +
	"`/previous` — vuelve a la canción anterior.\n" +
	"`/queue pagina:<n>` — muestra diez canciones por página y la duración total.\n" +
	"`/shuffle` — mezcla las canciones en espera.\n" +
	"`/remove posicion:<n>` — quita una canción de la cola.\n" +
	"`/move origen:<n> destino:<n>` — cambia una canción de posición.\n" +
	"`/skipto posicion:<n>` — salta a una canción en espera.\n" +
	"`/clearqueue confirmar:<sí>` — vacía la cola tras confirmarlo.\n" +
	"`/stop` o `/leave` — detiene la música y desconecta.\n" +
	"`/help` — muestra esta ayuda.\n\n" +
	"Las posiciones empiezan en 1. Los comandos de música se usan en el canal asignado; la ayuda está disponible en cualquier canal de texto."

func helpMessage() string {
	return helpMessageText
}
