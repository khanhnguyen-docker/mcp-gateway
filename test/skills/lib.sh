# Shared setup for the skills scripts: build the CLI, start a local registry,
# isolate HOME with the skills feature on, and push/pull/add the fixtures.
# Callers set a trap that calls skills_cleanup.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
FIXTURES=$ROOT/pkg/skills/testdata
WORK=$(mktemp -d)
BIN=$WORK/docker-mcp
REGISTRY=""
GATEWAY_PID=""

skills_cleanup() {
  [ -n "$GATEWAY_PID" ] && kill "$GATEWAY_PID" >/dev/null 2>&1 || true
  [ -n "$REGISTRY" ] && docker rm -f "$REGISTRY" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}

skills_setup() {
  echo "- Building docker-mcp"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/docker-mcp)

  echo "- Starting local registry"
  REGISTRY=$(docker run -d --rm -p 127.0.0.1::5000 registry:2)
  local port
  port=$(docker port "$REGISTRY" 5000/tcp | head -n1 | sed 's/.*://')
  for _ in $(seq 1 30); do curl -fs "http://127.0.0.1:$port/v2/" >/dev/null && break; sleep 0.5; done
  REG=127.0.0.1:$port

  # Isolated HOME (sqlite store, blob store) and docker config with the flag on.
  # The daemon address lives in the real HOME's docker context; carry it over.
  export DOCKER_HOST=${DOCKER_HOST:-$(docker context inspect -f '{{.Endpoints.docker.Host}}')}
  export HOME=$WORK/home DOCKER_CONFIG=$WORK/home/.docker DOCKER_MCP_IN_CONTAINER=1
  mkdir -p "$DOCKER_CONFIG" "$HOME/.docker/mcp/catalogs"
  echo '{"features":{"skills":"enabled"}}' > "$DOCKER_CONFIG/config.json"
  echo 'registry: {}' > "$HOME/.docker/mcp/catalogs/empty.yaml"
}

# skills_push <dir> <publisher> <ref>: validate, push, pull, add every skill.
skills_push() {
  echo "- Pushing $1 as $2 -> $3"
  "$BIN" skill push "$1" "$3" --publisher "$2"
  "$BIN" skill pull "$3"
  for uri in $("$BIN" skill validate "$1" --publisher "$2" | jq -r '.[].uri'); do
    "$BIN" skill add "$3" "$uri" >/dev/null
  done
}

# skills_gateway: start a streaming gateway with no servers; sets GW_URL.
skills_gateway() {
  local port
  port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
  echo "- Starting gateway on port $port"
  "$BIN" gateway run --transport streaming --port "$port" --allow-unauthenticated --catalog empty.yaml > "$WORK/gateway.log" 2>&1 &
  GATEWAY_PID=$!
  GW_URL=http://127.0.0.1:$port
  for _ in $(seq 1 60); do curl -fs "$GW_URL/health" >/dev/null 2>&1 && return 0; sleep 0.5; done
  echo "gateway did not become healthy" >&2
  cat "$WORK/gateway.log" >&2
  return 1
}
