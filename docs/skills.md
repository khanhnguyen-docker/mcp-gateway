# Skills

Agent Skills ([agentskills.io](https://agentskills.io/specification)) distributed
through OCI catalogs and served by the gateway per
[SEP-2640](https://modelcontextprotocol.io/seps/2640-skills-extension).
Design: [feature-specs/skills-over-mcp.md](feature-specs/skills-over-mcp.md).

```bash
docker mcp feature enable skills
```

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
