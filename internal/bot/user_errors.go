package bot

import (
	"context"
	"errors"
	"strings"
)

func userFacingError(err error) string {
	if err == nil {
		return "Ocurrió un problema inesperado."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "La solicitud tardó demasiado. Intenta de nuevo o usa un enlace directo a la canción."
	}
	if errors.Is(err, context.Canceled) {
		return "La operación fue cancelada. Si detuviste la música, puedes volver a intentarlo."
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "spotify") && strings.Contains(message, "403"):
		return "Spotify rechazó el acceso. Si es una playlist, comprueba que sea pública y que la autorización de Spotify siga vigente; algunas listas no exponen metadatos públicos."
	case strings.Contains(message, "spotify") && (strings.Contains(message, "oauth") || strings.Contains(message, "token") || strings.Contains(message, "authorization")):
		return "No pude autenticarme con Spotify. Revisa la autorización de usuario desde `/spotify/login` y vuelve a intentarlo."
	case strings.Contains(message, "spotify") || strings.Contains(message, "playlist"):
		return "No pude leer esa canción o playlist de Spotify. Prueba con un enlace público de pista o playlist; las listas privadas no están disponibles."
	case strings.Contains(message, "no playable tracks") || strings.Contains(message, "no playable candidates"):
		return "No encontré audio reproducible para esa búsqueda. Prueba con otro título o pega un enlace directo de YouTube."
	case strings.Contains(message, "403") || strings.Contains(message, "forbidden"):
		return "La fuente de audio rechazó la solicitud. Prueba otra versión de la canción o un enlace distinto."
	case strings.Contains(message, "yt-dlp") || strings.Contains(message, "resolve media") || strings.Contains(message, "youtube"):
		return "No pude obtener el audio de esa fuente. Prueba otra versión o pega un enlace directo de YouTube."
	default:
		technicalMarkers := []string{"error", "failed", "failure", "returned", "resolve", "decode", "request", "http", "yt-dlp", "youtube"}
		technical := false
		for _, marker := range technicalMarkers {
			if strings.Contains(message, marker) {
				technical = true
				break
			}
		}
		if len(err.Error()) <= 220 && !technical {
			return err.Error()
		}
		return "No pude completar la solicitud. Intenta de nuevo; si persiste, revisa los logs del bot para ver el detalle."
	}
}
