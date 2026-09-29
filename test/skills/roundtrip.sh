#!/usr/bin/env bash
# Push the skill fixtures to a local registry, pull them back, and diff the
# manifest digests reported by `skill validate` against `skill ls`.
source "$(dirname "$0")/lib.sh"
trap skills_cleanup EXIT
skills_setup

expected=$WORK/expected
actual=$WORK/actual
: > "$expected"

for spec in "simple fixtures simple" "full fixtures full" "publishers/acme acme acme/skills" "publishers/globex globex globex/skills"; do
  set -- $spec
  "$BIN" skill validate "$FIXTURES/$1" --publisher "$2" | jq -r '.[] | "\(.uri) \(.manifestDigest) ok"' >> "$expected"
  skills_push "$FIXTURES/$1" "$2" "$REG/$3:v1"
done

"$BIN" skill ls --json | jq -r '.[] | "\(.uri) \(.currentDigest) \(.status)"' | sort > "$actual"
sort -o "$expected" "$expected"

echo "- Comparing manifests"
if diff -u "$expected" "$actual"; then
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
