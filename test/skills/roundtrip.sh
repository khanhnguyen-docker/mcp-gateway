#!/usr/bin/env bash
# Push the skill fixtures to a local registry, pull them back, and diff the
# manifest digests reported by `skill validate` against `skill ls`.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
FIXTURES=$ROOT/pkg/skills/testdata
WORK=$(mktemp -d)
BIN=$WORK/docker-mcp
trap 'docker rm -f "$REGISTRY" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

echo "- Building docker-mcp"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/docker-mcp)

echo "- Starting local registry"
REGISTRY=$(docker run -d --rm -p 127.0.0.1::5000 registry:2)
PORT=$(docker port "$REGISTRY" 5000/tcp | head -n1 | sed 's/.*://')
for _ in $(seq 1 30); do curl -fs "http://127.0.0.1:$PORT/v2/" >/dev/null && break; sleep 0.5; done
REG=127.0.0.1:$PORT

# Isolated HOME (sqlite store, blob store) and docker config with the flag on.
export HOME=$WORK/home DOCKER_CONFIG=$WORK/home/.docker DOCKER_MCP_IN_CONTAINER=1
mkdir -p "$DOCKER_CONFIG"
echo '{"features":{"skills":"enabled"}}' > "$DOCKER_CONFIG/config.json"

expected=$WORK/expected
actual=$WORK/actual
: > "$expected"

push() { # <dir> <publisher> <ref>
  echo "- Pushing $1 as $2 -> $3"
  "$BIN" skill validate "$1" --publisher "$2" | jq -r '.[] | "\(.uri) \(.manifestDigest)"' >> "$expected"
  "$BIN" skill push "$1" "$3" --publisher "$2"
  "$BIN" skill pull "$3"
  for uri in $("$BIN" skill validate "$1" --publisher "$2" | jq -r '.[].uri'); do
    "$BIN" skill add "$3" "$uri" >/dev/null
  done
}

push "$FIXTURES/simple"            fixtures "$REG/simple:v1"
push "$FIXTURES/full"              fixtures "$REG/full:v1"
push "$FIXTURES/publishers/acme"   acme     "$REG/acme/skills:v1"
push "$FIXTURES/publishers/globex" globex   "$REG/globex/skills:v1"

"$BIN" skill ls --json | jq -r '.[] | "\(.uri) \(.currentDigest) \(.status)"' | sort > "$actual"
sort -o "$expected" "$expected"
sed 's/$/ ok/' "$expected" > "$expected.ok"

echo "- Comparing manifests"
if diff -u "$expected.ok" "$actual"; then
  echo "OK: $(wc -l < "$actual" | tr -d ' ') skills round-tripped with identical manifests"
else
  echo "FAIL: manifests differ" >&2
  exit 1
fi

# Every pulled blob rehashes to its name.
echo "- Verifying blob store"
find "$HOME/.docker/mcp/skills/sha256" -type f | while read -r f; do
  [ "$(shasum -a 256 "$f" | cut -d' ' -f1)" = "$(basename "$f")" ] || { echo "FAIL: $f digest mismatch" >&2; exit 1; }
done
echo "OK: blob store verified"
