# Skills over MCP: working notes

Local-only work on `feat/skills-over-mcp`. Nothing here is pushed. Text that
would normally go in a PR body goes here, one heading per Part.

## Part 1: design doc

Added `docs/feature-specs/skills-over-mcp.md`.

Patterns followed:
- Prose layout copied from `docs/feature-specs/catalog-management/feature-spec.md`
  (Overview, Background, decisions with rationale, tables for surfaces).

Deviations from the kickoff prompt, found while reading the code, each also
recorded in the spec:

1. **D6 cannot be done with go-sdk middleware.** go-sdk v1.4.1 validates the
   method name in `checkRequest` (`mcp/shared.go`) before the receiving
   middleware chain runs, and the streamable HTTP handler (`mcp/streamable.go`)
   returns HTTP 400 for unknown methods. go-sdk `main` still has no skills types.
   Proposed: a JSON-RPC shim in front of the SDK (stdio `Connection` wrapper,
   streaming `http.Handler`) sharing one dispatcher; `resources/read` for
   `skill://` stays in middleware because that method is known to the SDK.
2. **`pkg/catalog/identity` does not exist in this repo.** RFC 8785 is
   available from `github.com/cyberphone/json-canonicalization`, already vendored
   as an indirect dependency of sigstore. Using it moves the module to a direct
   requirement; no new module (D12).
3. **No signing code exists.** `pkg/signatures` only verifies Docker's key against
   `mcp/signatures`, and catalog artifacts are not verified on pull today.
   `skill push` therefore does not sign; verification happens where server
   images are verified (gateway `--verify-signatures`).
4. **`pkg/skills` will not import `pkg/oci`.** `pkg/oci` pulls in `pkg/desktop`;
   the format layer does not need OCI types, so layer descriptors live in
   `pkg/catalog_next`. Stricter than D1, not looser.
5. **`skill add` takes two arguments** (`<catalog-ref> <name-or-uri>`) instead
   of one, because a skill path has several segments and the existing
   `catalog://<ref>/<server>` parsing cannot split ref from path unambiguously.

Checks: no Go files changed in Part 1, so `make lint` / `make test` were not run.
They run at STOP 2.

## Part 2: pkg/skills, catalog artifact, CLI

Patterns followed:
- `pkg/catalog_next/push.go` and `pull.go` for the OCI flow; `pkg/oci/artifacts.go`
  for layer push (extended with `PushArtifactWithLayers`, old signature kept).
- `pkg/db/catalog.go` + `migrations/006_pull_record.up.sql` for the new tables
  and DAO methods; `pkg/db/workingset_test.go` `setupTestDB` for tests.
- `cmd/docker-mcp/commands/catalog_next.go` for the cobra layout;
  `feature.go` for the `skills` flag; `root.go` gates the command on it.
- `pkg/gateway/pull.go`'s `verifyDockerImageSignatures` var pattern for the
  test-overridable blob dir (`skillBlobDir`).

Decisions taken here (say so if you want any changed):
- `pkg/skills` imports only stdlib, `gopkg.in/yaml.v3`, and
  `github.com/cyberphone/json-canonicalization` (promoted indirect -> direct in
  go.mod, vendor unchanged). Not `pkg/oci`: layer descriptors live in
  `catalog_next.SkillEntry`.
- The "over the file limit" fixture is generated in the test (513 one-byte
  files, and a 16 MiB + 1 blob) instead of being checked in.
- `skill validate` always prints JSON; no `--json` no-op flag.
- `skill pull` is `catalog pull` plus blob download: skills ride in the same
  artifact, so one code path (`pullCatalog`) stores both.
- Blob store: `~/.docker/mcp/skills/sha256/<hex>`, files 0444, rehashed on
  every `ReadSkillFile`.
- `oci.GetArtifactDigest` (the catalog's local identity) still hashes a
  one-layer manifest; the registry manifest digest of a skills artifact differs
  from it. Pre-existing meaning ("content identity"), left alone.

Local toolchain note: `go build ./...` fails on Go 1.27 in a vendored grpc file
(unrelated, also on `upstream/main`). Everything here was built and tested with
`GOTOOLCHAIN=go1.25.12`, the version pinned in go.mod and the Dockerfile.

Checks run: `go test ./pkg/skills ./pkg/catalog_next ./pkg/db`,
`make skills-roundtrip` (5 skills, identical manifests, blob store rehashed),
`make lint-darwin`, `go test -short ./...` (results in the STOP 2 report).
