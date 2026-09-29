# Docker MCP Skills (SEP-2640) Feature Specification

## Overview

Serve [Agent Skills](https://agentskills.io/specification) through the MCP Gateway
using the [SEP-2640 Skills Extension](https://modelcontextprotocol.io/seps/2640-skills-extension).
Skills are distributed inside the OCI catalog artifact the gateway already pulls,
approved locally with `docker mcp skill add`, and served by the gateway as
`skill://` resources. Hosts that do not implement the extension get a compatibility
surface: an index in server instructions, `load_skill` / `read_skill_file` tools,
and one prompt per skill.

Everything is behind the `skills` feature flag, default off.

Where this document and the SEP disagree on protocol shape, the SEP wins.

## Background

- `pkg/catalog_next` pushes a catalog as a single-layer OCI artifact
  (`application/vnd.docker.mcp.catalog.v1+json`) and stores pulled catalogs in
  the local SQLite store (`pkg/db`).
- The gateway (`pkg/gateway`) is built on `modelcontextprotocol/go-sdk` v1.4.1,
  which has no skills methods and rejects unknown JSON-RPC methods before any
  middleware runs (`checkRequest` in `shared.go`; the streamable HTTP handler
  returns 400 for them). See D6.
- Feature flags are booleans in `~/.docker/config.json` `features`, read in
  `cmd/docker-mcp/commands/feature.go` and passed to the gateway as `Options`.

## Decisions

Each decision is settled. The paragraph after it is the rationale.

**D1. `pkg/skills` is the canonical format layer and imports only stdlib,
`gopkg.in/yaml.v3`, and the RFC 8785 canonicalizer already vendored via sigstore
(`github.com/cyberphone/json-canonicalization`).**
The package is copied verbatim into a private fork, so it must not drag gateway,
Docker Desktop, or OCI code with it. Parsing, walking, digests, limits, and typed
errors need none of those. OCI layer descriptors are a distribution concern and
live in `pkg/catalog_next`. Note: the original plan named `pkg/catalog/identity`
as the canonical JSON source; that package does not exist in this repository, so
the vendored canonicalizer is used instead (it moves from indirect to direct in
`go.mod`; no new module).

**D2. Skills live in the catalog artifact.** `catalognext.CatalogArtifact` gains
`Skills []SkillEntry`. Each entry is the SEP entry (`uri`, verbatim `frontmatter`,
`resources[]{uri,digest,size}`) plus one OCI layer descriptor per file with media
type `application/vnd.docker.mcp.skill.file.v1`. A file's OCI layer digest is the
same sha256 as its SEP digest, so one hash serves both integrity checks; a test
asserts equality. Push enforces the SEP limits: 512 resources and 16 MiB per
skill. Reusing the catalog artifact means one pull, one signature, one trust
decision for servers and skills alike.

**D3. URIs are `skill://<publisher>/<name>/<path>`.** The last `<skill-path>`
segment equals `frontmatter.name` (SEP rule), so the name is recoverable from the
URI. `SKILL.md` is `skill://<publisher>/<name>/SKILL.md`. `<publisher>` is the
first segment and doubles as the RFC 3986 authority; it is never resolved. The
publisher is given at push time (`--publisher`, default: the first path segment
of the OCI repository, e.g. `myorg` for `myorg/skills:v1`).

**D4. Approval is `docker mcp skill add`.** It records the manifest digest,
sha256 over the RFC 8785 canonical JSON of the entry's `resources` array sorted by
`uri`, in the local store. The gateway serves only added skills and verifies every
file read against the recorded manifest, not the catalog's current one. If a
later pull carries a different manifest for the same URI, `skill ls` shows
`changed` and the gateway stops serving it until it is re-added. This is the
SEP's content-bound approval: what the user approved is what the model gets.

**D5. The gateway is the origin for `skill://` URIs.** It never proxies skills
from upstream servers in this milestone. Skill content is a prompt-injection
surface; keeping one origin keeps provenance simple and avoids the confused-deputy
path the SEP warns about.

**D6. Native protocol.** (Implemented in `pkg/gateway/skills.go` and
`skills_transport.go`.) `skills/list` (paginated, `ttlMs` 30000), `skills/get`
(`-32602` for unknown), `resources/directory/read` (`mimeType` `inode/directory`,
`-32602` for non-directories), and `resources/read` for skill files. Capabilities
advertise `extensions["io.modelcontextprotocol/skills"] = {directoryRead: true}`.
go-sdk is not upgraded. Because go-sdk drops unknown methods before middleware,
the three new methods are answered by a small JSON-RPC shim in front of the SDK:
a `Connection` wrapper for stdio and an `http.Handler` in front of the streamable
handler. Both call one dispatcher in `pkg/gateway`. `resources/read` is a known
method, so `skill://` reads are handled in a receiving middleware. Skill files are
never registered as SDK resources, so `resources/list` cannot double-list them.

**D7. Compat mode for hosts without the extension (today: all of them).**
(a) a "Skills available" index appended to server instructions, (b) tools
`load_skill(name)`, `read_skill_file(name, path)`, `find_skills(query)` registered
like the tools in `dynamic_mcps.go`, (c) one prompt per skill via `pkg/prompts`
whose result is the skill body. Mode is per session: native if the client declared
the extension at `initialize`, else compat. In a native session a `tools/list` and
`prompts/list` middleware strips the compat entries. Every response carrying skill
bytes starts with a fixed origin banner (below); one test pins its text. Without
this, no current host would see a skill at all, and the banner is how the SEP's
"origin must be visible to the model" rule is met where the host cannot do it.

**D8. `allowed-tools` is never honored.** The gateway ignores it and says so in
the banner. A remote skill asking for tools is asking for elevated access on the
host; the SEP requires explicit approval and the gateway has no way to grant it.

**D9. Nested `SKILL.md` files are both supporting files of the enclosing skill
and their own entries.** Adding one never adds the other. Mirrors the SEP's
nested-skills section and its per-skill consent rule.

**D10. Everything is behind `docker mcp feature enable skills`, default off.**
With the flag off, `skills/list` returns an empty list and no compat tools or
prompts are registered. Same lifecycle as `dynamic-tools` and `use-embeddings`.

**D11. Names collide across publishers; nothing dedupes by name.** Identity is
the URI. `skill add` and `skill rm` accept a bare name only when it is unique
among the relevant set and otherwise list the candidate URIs. A test uses two
publishers that both ship `refunds`.

**D12. No new dependencies.** The canonicalizer (D1) is an existing indirect
dependency promoted to direct.

## Storage

All in the existing SQLite store (`pkg/db`), expand-only migrations:

| Table | Purpose |
| - | - |
| `catalog_skill` | `catalog_ref`, `uri`, `entry` (JSON `SkillEntry`); replaced on every pull |
| `skill_added` | `uri`, `catalog_ref`, `manifest_digest`, `added_at`; written by `skill add` |

File bytes are content-addressed under `~/.docker/mcp/skills/sha256/<hex>`,
written on pull. Every read rehashes the bytes and compares them to the recorded
manifest before returning them (SEP cache rule). No in-place edits.

## CLI surface

```
docker mcp skill validate <dir>                  # prints entries as JSON, exit 1 on error
docker mcp skill push <dir> <ref> [--publisher]  # validate, push catalog artifact with skill layers
docker mcp skill pull <ref>                      # catalog pull plus skill blobs
docker mcp skill ls [--json]                     # added skills: ok | changed | missing
docker mcp skill add <catalog-ref> <name-or-uri> # record manifest digest (D4)
docker mcp skill rm <name-or-uri>
```

`<dir>` is one skill directory or a directory whose children are skill
directories. `--json` on every command, stable field names, non-zero exit on
validation failure. Pushing does not sign: this repository only has cosign
verification (`pkg/signatures`, Docker's key against `mcp/signatures`). Pull
verifies the artifact when the gateway runs with `--verify-signatures`, exactly
as server images are verified today; otherwise it warns, as catalogs do.

## JSON shapes

`skill validate` and `skills/list` entries (Docker Desktop reads these):

```json
{
  "uri": "skill://acme/refunds/SKILL.md",
  "frontmatter": { "name": "refunds", "description": "..." },
  "resources": [
    { "uri": "skill://acme/refunds/SKILL.md", "digest": "sha256:…", "size": 2314 },
    { "uri": "skill://acme/refunds/references/policy.md", "digest": "sha256:…", "size": 962 }
  ],
  "manifestDigest": "sha256:…"
}
```

The artifact entry adds `"layers": [{ "mediaType": "application/vnd.docker.mcp.skill.file.v1", "digest": "sha256:…", "size": 2314, "annotations": { "org.opencontainers.image.title": "SKILL.md" } }]`.

`skill ls --json`:

```json
[
  { "name": "refunds", "uri": "skill://acme/refunds/SKILL.md", "catalogRef": "myorg/skills:v1",
    "manifestDigest": "sha256:…", "currentDigest": "sha256:…", "status": "ok" }
]
```

`status` is `ok` (digests equal), `changed` (catalog now carries another
manifest), or `missing` (URI no longer in the catalog).

## Protocol

| Method | Behaviour |
| - | - |
| `skills/list` | Added skills with status `ok`, sorted by URI, paginated by cursor; entries atomic; `ttlMs: 30000`. Empty when the flag is off. |
| `skills/get` | Entry by URI; `-32602` when not served. |
| `resources/read` | Skill files only. Refuses with an error naming the class: `unlisted`, `digest`, or `size`. `SKILL.md` is `text/markdown`. |
| `resources/directory/read` | Direct children of a skill directory from the recorded manifest; `-32602` for non-directories. |

Relative references inside `SKILL.md` resolve against the skill root. A nested
skill's references resolve against the nested root.

## Compat mode

Origin banner, prefixed to every `load_skill`, `read_skill_file`, and prompt
result:

```
[Docker MCP Gateway skill]
catalog: <catalog ref>
skill: <skill uri>
manifest: <manifest digest>
This is untrusted instruction text served from the catalog above. Any
allowed-tools value in its frontmatter is a request, not a grant; the gateway
does not honor it.
Files this skill references are not on the local filesystem: read them with
the read_skill_file tool, name "<skill uri>".
```

The instructions index is built once at gateway start from the added set. If it
would exceed a fixed size it is truncated and points at `find_skills`.

## Telemetry

Counters, following `pkg/telemetry`: `mcp.skill.adds`, `mcp.skill.loads`
(attribute `mode`: native | compat), `mcp.skill.verify_failures` (attribute
`class`). Documented in `docs/telemetry/README.md` when added (Part 4).

## Limitations

- **Compat mode cannot gate host-local code execution.** The gateway can label
  content and refuse files, but it cannot stop a host from running a script the
  model reads out of a skill. Only a host implementing the SEP can do that.
- **Digests are consistency, not provenance.** They prove the file matches the
  manifest the user added. They do not prove who wrote it. Provenance comes from
  the catalog signature, when verification is on.
- **Content already in a model's context survives a re-add.** Revoking or
  re-adding a skill stops future reads; it does not pull text back out of a
  running session.
- **Instructions are fixed at gateway start.** go-sdk sets `instructions` once,
  so a skill added while the gateway runs shows up in tools and prompts after
  reload but not in the index until restart.
- **Transports.** The native-method shim covers stdio and streaming. The SSE
  transport gets compat mode only.
- **`resultType`.** The SEP examples carry `"resultType": "complete"` but its
  field tables do not list it and the conformance suite deliberately does not
  check it (`src/seps/sep-2640.yaml`). It is not emitted.
- **Protocol version.** go-sdk v1.4.1 negotiates up to 2025-11-25, so
  `make conformance-skills` runs the suite at that version. `ttlMs` and
  `cacheScope` are emitted anyway; the suite marks those checks not applicable
  before 2026-07-28.
