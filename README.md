# Discord Music Bot (Go)

Bot de música para Discord implementado en Go. Usa comandos de texto con prefijo `!`, mantiene una cola concurrente por servidor y reproduce audio de YouTube mediante `yt-dlp`, `ffmpeg` y Opus. Los enlaces de pistas y playlists públicas de Spotify se convierten en búsquedas de YouTube usando metadatos de Spotify.

## Funciones

- La actividad del bot muestra “Escuchando !play”. Los comandos están disponibles como mensajes con prefijo `!` y como comandos slash de Discord.
- `!play cancion`: URL de YouTube, enlace Spotify de pista/playlist o búsqueda de texto.
- `!help`, `!ayuda` o `/help`: muestra la lista de comandos y su uso en cualquier canal de texto.
- `!join`: conecta o mueve el bot al canal de voz donde estás; si había una canción activa, la reinicia en el canal nuevo.
- Los comandos de música quedan vinculados al canal de texto del primer `!play`; si se envían desde otro canal, el bot indica dónde usarlos. `!stop` o `!leave` libera el canal asignado.
- Las playlists de Spotify comienzan a reproducirse al resolver la primera canción; el resto se resuelve en grupos ordenados en segundo plano. `!stop` cancela esa carga.
- `!skip`: cancela la pista activa y continúa con la siguiente.
- `!pause`: alterna pausa/reanudación.
- `!previous`: vuelve a la canción anterior; si no hay historial, reinicia la actual.
- `!shuffle`: mezcla las canciones que están en espera.
- `!queue [página]` o `/queue`: muestra diez canciones por página, la canción actual y la duración total de la cola. Para cambiar de página usa, por ejemplo, `!queue 2` o `/queue pagina:2`.
- `!remove <posición>`: quita una canción en espera.
- `!move <origen> <destino>`: mueve una canción a otra posición de la cola.
- `!clearqueue confirmar` o `/clearqueue` con `confirmar` activado: confirma antes de vaciar las canciones en espera y cancelar la carga pendiente de playlists; conserva la canción actual y AutoPlay.
- `!skipto <posición>`: salta a esa canción en espera, descartando las anteriores. Las posiciones de estos comandos empiezan en 1 y coinciden con `!queue`; no incluyen la canción actual.
- `!stop` y `!leave`: limpian la cola y desconectan el bot; el panel queda como confirmación con quien detuvo la música y el enlace de la pista.
- Las búsquedas de audio consultan hasta cinco resultados de YouTube en modo plano, añaden `lyrics` cuando corresponde y priorizan títulos lyrics/letras, audio y visualizer. Se descartan títulos identificados como videoclip oficial; la clasificación depende del título publicado y puede equivocarse. Las URL directas empiezan con el video enlazado. Para Spotify, se conserva primero artista y título; las variantes de búsqueda quedan como fallback.
- Cuando la canción actual ya entrega audio, el bot extrae anticipadamente la fuente de la siguiente y conserva el resultado para reproducirla con `--load-info-json`, evitando repetir la extracción. Se reutiliza durante cinco minutos, con un máximo de ocho pistas y dos extracciones simultáneas. Shuffle prepara la nueva siguiente canción; Skip conserva la extracción si corresponde a la pista que pasa a reproducirse y espera el trabajo que ya esté en curso. Stop y Previous cancelan los trabajos pendientes. Una pista nueva todavía puede requerir extracción si se salta inmediatamente. FFmpeg usa menos análisis inicial y páginas Ogg cortas; los logs distinguen el primer byte de la fuente del primer frame Opus para medir la mejora real.
- Cada `!play` activa el indicador de escritura y muestra un mensaje de carga mientras busca y conecta la voz. Ese mismo mensaje se transforma en el panel persistente, que indica «Cargando audio» hasta recibir el primer frame de música. Los títulos visibles omiten etiquetas como Official Video o Lyrics; para Spotify se usan el nombre y artista originales. En el panel, la pista enlaza a su fuente y “Requested By” enlaza al perfil del solicitante; también se muestran duración y autor. Sus botones permiten volver, pausar/reanudar, saltar, mezclar, repetir, activar AutoPlay, detener y consultar la playlist de forma privada. Los botones permanecen habilitados; si una acción no está disponible, el bot explica el motivo. Loop y AutoPlay comienzan desactivados.
- Pausar no tiene un límite de tiempo configurado: el bot suspende temporalmente `yt-dlp` y FFmpeg mientras envía silencio a Discord, y los reanuda al quitar la pausa.
- Desconexión automática después de `AUTO_LEAVE_SECONDS` sin personas en el canal.
- Endpoints HTTP `/healthz` y `/readyz` para Docker/Kubernetes.
- Procesamiento concurrente por servidor; las sesiones no comparten colas.

Las playlists Spotify se limitan a 500 canciones por solicitud. Si Spotify devuelve HTTP 403 al leer una playlist, el bot intenta obtener hasta 100 canciones desde los metadatos públicos de su página embed; este fallback puede no estar disponible o estar incompleto, y no permite leer playlists privadas. Si no hay metadatos públicos, se conserva el error de Spotify. Las búsquedas de YouTube se resuelven con hasta cuatro solicitudes concurrentes, manteniendo el orden original de la playlist. Spotify solo aporta metadatos; la búsqueda y reproducción se hacen en YouTube. Usa el bot y las fuentes de audio de acuerdo con los términos de servicio aplicables.

## Requisitos

- Go 1.27 o superior.
- Docker y Docker Compose (recomendado para ejecutar localmente).
- Para ejecución nativa: `yt-dlp`, `ffmpeg` con soporte de `libopus` y el filtro `azmq` (con ZeroMQ) y Python 3.
- Una aplicación/bot de Discord y, para resolver Spotify, una aplicación de Spotify Developer.

## Credenciales de Discord

1. Crea una aplicación en [Discord Developer Portal](https://discord.com/developers/applications).
2. En **Bot**, crea el bot y copia el token. No publiques ni compartas ese token.
3. En **OAuth2 > URL Generator**, selecciona el scope `bot`. Invítalo con permisos para ver canales, leer y enviar mensajes, y conectarse y hablar en canales de voz.
4. En **Bot > Privileged Gateway Intents**, habilita **Message Content Intent**. El bot lo necesita para reconocer mensajes con prefijo como `!play`; `Guilds`, `GuildVoiceStates` y `GuildMessages` son intents no privilegiados.

Al iniciar, el bot registra y sincroniza los comandos slash globales de música. Discord puede tardar un tiempo en mostrarlos después de la primera actualización; también puedes seguir escribiendo los comandos con prefijo `!` en el canal de texto.

## Credenciales de Spotify

1. Crea una aplicación en [Spotify Developer Dashboard](https://developer.spotify.com/dashboard).
2. Copia su **Client ID** y **Client Secret**.
3. Las búsquedas de pistas usan Client Credentials. Para leer playlists, el bot requiere autorización de usuario mediante OAuth; la primera vez se autoriza desde el navegador y el refresh token se guarda en almacenamiento persistente con permisos restringidos. La aplicación en Development Mode debe tener un propietario con Premium y cada usuario que autorice debe estar en User Management.

## Ejecución local con Docker Compose

Desde la raíz del proyecto:

```powershell
Copy-Item .env.example .env
```

Edita `.env` y define `DISCORD_TOKEN`, `SPOTIFY_CLIENT_ID` y `SPOTIFY_CLIENT_SECRET`. No guardes el `.env` en Git; está excluido por `.gitignore`. `DISCORD_CLIENT_ID` ya no es necesario.

En Spotify Developer Dashboard, agrega como Redirect URI exacta `http://127.0.0.1:18080/spotify/callback`. Después de iniciar el bot, abre `http://127.0.0.1:18080/spotify/login` en el mismo equipo y autoriza con una cuenta Premium incluida en **User Management**. El refresh token queda en el volumen Docker `spotify-data`; no lo compartas ni lo añadas al `.env`.

Construye e inicia el bot:

```powershell
docker compose up --build -d
docker compose logs -f bot
```

Verifica la sonda local:

```powershell
Invoke-WebRequest http://localhost:8080/healthz
Invoke-WebRequest http://localhost:8080/readyz
```

Para autorizar Spotify, el volumen y el puerto local `18080` quedan disponibles con el servicio Compose; el endpoint de autorización redirige a Spotify y el callback vuelve a loopback.

Detén el servicio con `docker compose down`.

## Ejecución nativa

Instala las herramientas del sistema indicadas en [Requisitos](#requisitos), configura en la sesión las mismas variables del `.env` y ejecuta:

```powershell
go mod download
go run ./cmd/bot
```

En PowerShell, se pueden cargar las variables del archivo `.env` en la sesión actual con:

```powershell
Get-Content .env | ForEach-Object {
  if ($_ -match '^\s*([^#=]+)=(.*)$') {
    [Environment]::SetEnvironmentVariable($Matches[1].Trim(), $Matches[2].Trim(), 'Process')
  }
}
go run ./cmd/bot
```

La aplicación usa FFmpeg para codificar audio Opus, por lo que no requiere CGO ni bibliotecas Opus para compilar Go.

## Despliegue en Kubernetes

1. Construye la imagen y publícala en un registry accesible por el clúster:

   ```powershell
   docker build -t ghcr.io/your-org/discord-music-bot:1.0.0 .
   docker push ghcr.io/your-org/discord-music-bot:1.0.0
   ```

2. Actualiza `image` en `deployments/k8s/deployment.yaml` con la imagen publicada.
3. Crea el namespace:

   ```powershell
   kubectl create namespace discord
   ```

4. **No apliques `secret.yaml` sin reemplazar los valores `REPLACE_BEFORE_APPLYING`.** Para producción, es preferible crear el Secret desde un gestor externo o de forma interactiva y no almacenar credenciales en Git. Ejemplo:

   ```powershell
   kubectl create secret generic discord-music-bot -n discord `
     --from-literal=DISCORD_TOKEN='<token>' `
     --from-literal=SPOTIFY_CLIENT_ID='<spotify-client-id>' `
     --from-literal=SPOTIFY_CLIENT_SECRET='<spotify-client-secret>'
   ```

   Alternativamente, sustituye los marcadores en `secret.yaml` de forma segura antes de aplicarlo.

5. Aplica configuración, Deployment y Service:

   ```powershell
   kubectl apply -f deployments/k8s/configmap.yaml
   kubectl apply -f deployments/k8s/spotify-pvc.yaml
   kubectl apply -f deployments/k8s/deployment.yaml
   kubectl apply -f deployments/k8s/service.yaml
   kubectl rollout status deployment/discord-music-bot -n discord
   ```

El `Service` es interno y expone las sondas HTTP; el bot inicia conexiones salientes a Discord, Spotify y YouTube. No se requiere un Service UDP entrante. Asegura que la política de red permita salida HTTPS y tráfico de voz/UDP hacia Discord. Para iniciar OAuth usa `kubectl port-forward -n discord service/discord-music-bot 18080:8080` y abre `http://127.0.0.1:18080/spotify/login`; configura la misma Redirect URI en Spotify Dashboard. El PVC conserva el refresh token entre reinicios. El manifiesto asigna una réplica porque la cola y la conexión de voz son estado local por proceso; no escales réplicas sin añadir coordinación y particionamiento por servidor.

El Deployment configura requests/limits, `livenessProbe`, `readinessProbe`, usuario no root y filesystem de solo lectura. Ajusta CPU/memoria según carga y límites de concurrencia del clúster.

## Despliegue económico en AWS ECS

Hay una configuración Terraform en [deployments/aws/terraform/](./deployments/aws/terraform/). Crea un servicio ECS Fargate ARM64 con una sola tarea de 0.25 vCPU y 0.5 GiB, una subnet pública para salida directa y sin NAT Gateway, balanceador ni puertos de entrada. Requiere una imagen en ECR y parámetros `SecureString` preexistentes en SSM Parameter Store. Sigue el [README de AWS](./deployments/aws/terraform/README.md) para configurar credenciales, construir/publicar la imagen y desplegar.

Este despliegue no es necesariamente gratuito: Fargate, la IPv4 pública, CloudWatch, almacenamiento ECR y tráfico de salida pueden generar cargos. En especial, el audio puede sumar transferencia de datos. Establece un presupuesto/alerta de costos antes de aplicar Terraform. La rotación de un secreto en SSM requiere forzar un nuevo deployment ECS para que la tarea lea su nuevo valor.

## Despliegue en Oracle Cloud Infrastructure

Hay una configuración Terraform en [deployments/oracle/terraform/](./deployments/oracle/terraform/) para una VM Ubuntu ARM64 `VM.Standard.A1.Flex` dimensionada en 2 OCPU y 12 GB, dentro de la cuota mensual publicada de Always Free. Instala Docker y configura el servicio para ejecutar una imagen ARM64 pública; el token de Discord y las credenciales opcionales de Spotify se escriben directamente en la VM y no se guardan en Terraform. Consulta el [README de OCI](./deployments/oracle/terraform/README.md) para construir la imagen, limitar SSH a tu IP, desplegar y configurar los secretos.

La capacidad Always Free puede no estar disponible en la región de origen y Oracle puede recuperar instancias que cumplan sus criterios de inactividad. El volumen de arranque consume la cuota de almacenamiento compartida de la cuenta. Revisa los límites y precios vigentes antes de aplicar; el Free Tier no garantiza disponibilidad continua ni cobertura de recursos que excedan la cuota restante.

## Variables de entorno

| Variable | Requerida | Predeterminado | Uso |
|---|---:|---|---|
| `DISCORD_TOKEN` | Sí | — | Token del bot de Discord |
| `SPOTIFY_CLIENT_ID` | No | — | Client ID de Spotify; requiere también el secret |
| `SPOTIFY_CLIENT_SECRET` | No | — | Client Secret de Spotify |
| `SPOTIFY_REDIRECT_URI` | No | `http://127.0.0.1:18080/spotify/callback` | Redirect URI registrada en Spotify Dashboard |
| `SPOTIFY_TOKEN_FILE` | No | `/var/lib/discord-music-bot/spotify-token.json` | Archivo protegido con el refresh token OAuth |
| `HTTP_ADDR` | No | `:8080` | Dirección HTTP para health checks |
| `AUTO_LEAVE_SECONDS` | No | `180` | Segundos sin usuarios antes de desconectar |

La aplicación no lee `.env` por sí sola; Docker Compose carga ese archivo mediante `env_file`. En ejecución nativa, exporta las variables en el entorno del proceso.

## Estructura

```text
cmd/bot/                 Punto de entrada y apagado ordenado
internal/audio/          Resolver yt-dlp y streaming FFmpeg/Opus
internal/bot/            Comandos, panel de botones, eventos de voz y health checks
internal/config/         Validación de variables de entorno
internal/player/         Cola segura, pausa, historial y aleatorio por servidor
internal/spotify/        OAuth de aplicación/usuario y metadatos Spotify
deployments/k8s/         ConfigMap, Secret de ejemplo, Deployment y Service
deployments/aws/terraform/ Infraestructura de bajo costo para una tarea ECS Fargate
deployments/oracle/terraform/ Infraestructura OCI Always Free para una VM ARM64
Dockerfile               Imagen multi-stage
docker-compose.yml       Ejecución local
```

## Notas operativas

- Los binarios `yt-dlp` y `ffmpeg` deben estar en `PATH`; errores de resolución/transcodificación quedan registrados por el bot.
- DisGo administra el protocolo Gateway/Voice y `dave-go` implementa el protocolo DAVE requerido por Discord. FFmpeg codifica Opus dentro de Ogg y la aplicación extrae sus paquetes para enviarlos por la conexión de voz; retiene los frames durante el handshake DAVE y los envía cuando la sesión E2EE está lista.
- Si una imagen anterior registra `websocket close 4017: E2EE/DAVE protocol required`, reconstruye y vuelve a desplegar la imagen: esa versión no tenía soporte DAVE. Esta migración agrega la compatibilidad; no requiere cambiar permisos ni habilitar intents privilegiados. Si el error persiste con la nueva imagen, verifica que el contenedor use el build nuevo y tenga salida de red HTTPS y UDP hacia Discord.
- La cola reside en memoria y se pierde al reiniciar el proceso. No se usa almacenamiento persistente.
- `/healthz` verifica que el proceso HTTP esté vivo; `/readyz` indica que la aplicación terminó su inicialización.
