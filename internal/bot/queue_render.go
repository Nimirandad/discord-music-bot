package bot

import (
	"fmt"
	"strings"
	"time"

	"discord-music-bot/internal/player"
)

const queuePageSize = 10

func renderQueue(snapshot player.Snapshot, page int) (string, error) {
	if page < 1 {
		return "", fmt.Errorf("Las páginas de la cola empiezan en 1.")
	}
	totalPages := (len(snapshot.Queue) + queuePageSize - 1) / queuePageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		return "", fmt.Errorf("La cola tiene %d página(s); usa `/queue pagina:%d` o una página menor.", totalPages, totalPages)
	}

	lines := make([]string, 0, queuePageSize+4)
	var totalDuration time.Duration
	unknownDurations := 0
	if snapshot.Current != nil {
		lines = append(lines, "**Reproduciendo:** "+truncateText(displayMusicTitle(*snapshot.Current), 180))
		totalDuration += snapshot.Current.Duration
		if snapshot.Current.Duration <= 0 {
			unknownDurations++
		}
	}
	for _, track := range snapshot.Queue {
		totalDuration += track.Duration
		if track.Duration <= 0 {
			unknownDurations++
		}
	}
	start := (page - 1) * queuePageSize
	end := min(start+queuePageSize, len(snapshot.Queue))
	for index := start; index < end; index++ {
		track := snapshot.Queue[index]
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, truncateText(displayMusicTitle(track), 180)))
	}
	if len(snapshot.Queue) == 0 && snapshot.Current == nil {
		return "", fmt.Errorf("La cola está vacía.")
	}
	if start == end && snapshot.Current != nil {
		lines = append(lines, "No hay canciones en espera en esta página.")
	}
	durationLabel := "Duración total (actual + espera): " + formatQueueDuration(totalDuration)
	if unknownDurations > 0 {
		durationLabel = fmt.Sprintf("Duración conocida: %s · %d sin duración", formatQueueDuration(totalDuration), unknownDurations)
	}
	lines = append(lines, fmt.Sprintf("Página %d/%d · %d en espera", page, totalPages, len(snapshot.Queue)), durationLabel)
	return truncateText(strings.Join(lines, "\n"), 1900), nil
}

func formatQueueDuration(duration time.Duration) string {
	if duration <= 0 {
		return "desconocida"
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}
