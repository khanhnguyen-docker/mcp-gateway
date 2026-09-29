# Skills

Agent Skills ([agentskills.io](https://agentskills.io/specification)) distributed
through OCI catalogs and served by the gateway per
[SEP-2640](https://modelcontextprotocol.io/seps/2640-skills-extension).
Design: [feature-specs/skills-over-mcp.md](feature-specs/skills-over-mcp.md).

The `skills` feature is on by default. Turn it off with
`docker mcp feature disable skills`.

## Commands

```bash
# Validate a skill directory (or a directory of skills); prints SEP-2640 entries
docker mcp skill validate ./refunds

# Push as a catalog artifact. URIs become skill://<publisher>/<name>/<path>.
docker mcp skill push ./refunds myorg/skills:v1            # publisher = myorg
docker mcp skill push ./skills  localhost:5000/s:v1 --publisher acme

# Pull the catalog and its skill files into ~/.docker/mcp/skills
docker mcp skill pull myorg/skills:v1

# Approve a skill: records the manifest digest the gateway will verify against
docker mcp skill add myorg/skills:v1 refunds
docker mcp skill add myorg/skills:v1 skill://myorg/refunds/SKILL.md   # when the name is ambiguous

# What is added and whether the catalog still matches
docker mcp skill ls [--json]        # status: ok | changed | missing

docker mcp skill rm refunds
```

A skill whose catalog entry changed after `add` shows `changed` and is not
served until added again. `allowed-tools` in frontmatter is never honored.

## Layout rules

- `SKILL.md` frontmatter needs `name` (equal to the directory name) and
  `description`; other fields pass through verbatim.
- At most 512 files and 16 MiB per skill.
- A nested `SKILL.md` is a file of the enclosing skill and a skill of its own;
  add each separately.

## Serving

With the feature on, `docker mcp gateway run` serves every added skill whose
manifest still matches its catalog:

- **Native** (client declares `io.modelcontextprotocol/skills` at initialize):
  `skills/list`, `skills/get`, `resources/directory/read`, and `resources/read`
  of `skill://` files. Each `SKILL.md` is also listed in `resources/list`.
- **Compat** (every other client): a "Skills available" index in the server
  instructions, the `load_skill`, `read_skill_file`, and `find_skills` tools,
  and one `skill-<publisher>-<name>` prompt per skill. Every response carrying
  skill text starts with a banner naming the catalog, URI, and manifest digest.

Every file read is rehashed against the manifest recorded by `skill add`. A
read of a file that is not in the manifest, or whose bytes changed, is refused
with an error naming the class: `unlisted`, `digest`, or `size`.

`make conformance-skills` runs the SEP-2640 scenarios from
[modelcontextprotocol/conformance](https://github.com/modelcontextprotocol/conformance)
against a gateway serving the fixtures (needs docker, node, jq, curl).

## Try it in Claude Desktop or Claude Code

```bash
docker mcp feature enable skills   # already on by default
docker mcp skill pull myorg/skills:v1
docker mcp skill add myorg/skills:v1 refunds
docker mcp client connect claude-desktop      # or: claude-code
```

Restart the client. The gateway's instructions list the skill; ask the model
to load it, or run the `skill-myorg-refunds` prompt where the client exposes
MCP prompts (Claude Desktop shows them in the + menu, Claude Code as
`/mcp__MCP_DOCKER__skill-myorg-refunds`). Supporting files come through
`read_skill_file`.
