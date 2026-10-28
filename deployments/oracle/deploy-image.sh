#!/usr/bin/env bash
set -euo pipefail

image="${1:-}"
if [[ ! "$image" =~ ^ghcr\.io/nimirandad/discord-music-bot:[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
  echo "Rejected image reference" >&2
  exit 2
fi

config=/etc/discord-music-bot-image
service=discord-music-bot
previous_image=""
if [[ -f "$config" ]]; then
  previous_image="$(sed -n 's/^CONTAINER_IMAGE=//p' "$config" | head -n 1)"
fi

write_image() {
  local reference="$1"
  local temporary
  temporary="$(mktemp /etc/discord-music-bot-image.XXXXXX)"
  printf 'CONTAINER_IMAGE=%s\n' "$reference" > "$temporary"
  chmod 0644 "$temporary"
  mv "$temporary" "$config"
}

wait_until_healthy() {
  local attempt health
  for attempt in $(seq 1 30); do
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' discord-music-bot 2>/dev/null || true)"
    if [[ "$health" == healthy ]] && curl -fsS --max-time 4 http://127.0.0.1:8080/healthz >/dev/null; then
      return 0
    fi
    sleep 5
  done
  return 1
}

rollback() {
  if [[ -n "$previous_image" ]]; then
    echo "Deployment failed; restoring previous image" >&2
    write_image "$previous_image"
    systemctl restart "$service"
  fi
}

write_image "$image"
if ! systemctl restart "$service" || ! wait_until_healthy; then
  rollback
  exit 1
fi

echo "Deployment healthy: $image"
