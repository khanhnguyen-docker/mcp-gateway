#!/usr/bin/env bash
# Run the SEP-2640 scenarios of github.com/modelcontextprotocol/conformance
# against a gateway serving the fixtures, then show a tampered file refused.
# CONFORMANCE_DIR points at a checkout of the conformance repo (cloned when
# missing). Needs docker, node/npm, jq, curl.
CONFORMANCE_DIR=${CONFORMANCE_DIR:-$HOME/.cache/mcp-conformance}
source "$(dirname "$0")/lib.sh"
trap skills_cleanup EXIT

if [ ! -d "$CONFORMANCE_DIR/src" ]; then
  echo "- Cloning modelcontextprotocol/conformance into $CONFORMANCE_DIR"
  git clone -q --depth 1 https://github.com/modelcontextprotocol/conformance.git "$CONFORMANCE_DIR"
fi
[ -d "$CONFORMANCE_DIR/node_modules" ] || (cd "$CONFORMANCE_DIR" && npm install --silent)

skills_setup
# The runner probes the first listed skill; full has subdirectories and a nested skill.
skills_push "$FIXTURES/full" fixtures "$REG/full:v1"
skills_gateway

# go-sdk v1.4.1 negotiates up to 2025-11-25, the last stateful version.
echo "- Running SEP-2640 conformance scenarios"
: > "$WORK/conformance.log"
for scenario in sep-2640-skills-enumeration sep-2640-skills-manifest sep-2640-skills-directory; do
  (cd "$CONFORMANCE_DIR" && npm start --silent -- server --url "$GW_URL/mcp" --scenario "$scenario" --spec-version 2025-11-25 --force) | tee -a "$WORK/conformance.log"
done
if grep -Eq 'FAILURE|SKIPPED: scenario|, [1-9][0-9]* failed' "$WORK/conformance.log"; then
  echo "FAIL: conformance reported failures" >&2
  exit 1
fi

# Negative case: a mutated file in the local store is refused on read.
echo "- Negative case: tampering with a stored file"
digest=$("$BIN" skill validate "$FIXTURES/full" --publisher fixtures | jq -r '.[0].resources[] | select(.uri=="skill://fixtures/full/references/policy.md") | .digest' | sed 's/^sha256://')
blob=$HOME/.docker/mcp/skills/sha256/$digest
chmod u+w "$blob"; printf 'tampered' > "$blob"

rpc() { # <headers...> ; body on stdin
  curl -s -D "$WORK/headers" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' "$@" "$GW_URL/mcp" \
    | sed -n 's/^data: //p; /^{/p' | head -n1
}
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"negative","version":"0"}}}' | rpc --data-binary @- >/dev/null
sid=$(grep -i '^mcp-session-id:' "$WORK/headers" | tr -d '\r' | cut -d' ' -f2)
echo '{"jsonrpc":"2.0","method":"notifications/initialized"}' | rpc -H "Mcp-Session-Id: $sid" --data-binary @- >/dev/null
out=$(echo '{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"skill://fixtures/full/references/policy.md"}}' | rpc -H "Mcp-Session-Id: $sid" --data-binary @-)
echo "$out"
echo "$out" | jq -e '.error.message | test("refused \\((digest|size)\\)")' >/dev/null && echo "OK: tampered file refused" || { echo "FAIL: tampered file was served" >&2; exit 1; }
