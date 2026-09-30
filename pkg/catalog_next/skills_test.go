package catalognext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/mcp-gateway/pkg/db"
	mcpoci "github.com/docker/mcp-gateway/pkg/oci"
	"github.com/docker/mcp-gateway/pkg/skills"
)

const fixtures = "../skills/testdata"

func useTempBlobDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := skillBlobDir
	skillBlobDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { skillBlobDir = old })
	return dir
}

// storeLayers writes pushed layers into the local store as pull would.
func storeLayers(t *testing.T, layers []mcpoci.ExtraLayer) {
	t.Helper()
	for _, l := range layers {
		p, err := skillBlobPath(l.Descriptor().Digest.String())
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, l.Data, 0o644))
	}
}

func TestLoadSkillsLayerDigestEqualsSEPDigest(t *testing.T) {
	entries, layers, err := LoadSkills(filepath.Join(fixtures, "full"), "fixtures")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Len(t, layers, 9, "7 files of full + 2 of the nested skill")

	for _, e := range entries {
		require.Len(t, e.Layers, len(e.Resources))
		for i, r := range e.Resources {
			assert.Equal(t, r.Digest, e.Layers[i].Digest.String(), r.URI)
			assert.Equal(t, r.Size, e.Layers[i].Size, r.URI)
			assert.Equal(t, SkillFileMediaType, e.Layers[i].MediaType)
		}
	}
	assert.Equal(t, "SKILL.md", entries[0].Layers[0].Annotations["org.opencontainers.image.title"])
}

func TestReadSkillFileVerifies(t *testing.T) {
	useTempBlobDir(t)
	entries, layers, err := LoadSkills(filepath.Join(fixtures, "simple"), "fixtures")
	require.NoError(t, err)
	storeLayers(t, layers)
	entry := entries[0].Entry

	data, dgst, err := ReadSkillFile(entry, entry.URI)
	require.NoError(t, err)
	assert.Equal(t, entry.Resources[0].Digest, dgst)
	assert.Contains(t, string(data), "name: simple")

	_, _, err = ReadSkillFile(entry, "skill://fixtures/simple/other.md")
	require.ErrorIs(t, err, skills.ErrUnlisted)

	// Mutating the stored blob is caught on the next read.
	p, err := skillBlobPath(entry.Resources[0].Digest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte("tampered"), 0o644))
	_, _, err = ReadSkillFile(entry, entry.URI)
	require.ErrorIs(t, err, skills.ErrSize)
	require.NoError(t, os.WriteFile(p, append(data[:len(data)-1], 'X'), 0o644))
	_, _, err = ReadSkillFile(entry, entry.URI)
	require.ErrorIs(t, err, skills.ErrDigest)
}

func TestSkillsRoundTripDbAndAdd(t *testing.T) {
	ctx := t.Context()
	t.Setenv("HOME", t.TempDir()) // AddSkill writes Claude Code stubs under $HOME/.claude when it exists
	dao := setupTestDB(t)

	acme, _, err := LoadSkills(filepath.Join(fixtures, "publishers", "acme"), "acme")
	require.NoError(t, err)
	globex, _, err := LoadSkills(filepath.Join(fixtures, "publishers", "globex"), "globex")
	require.NoError(t, err)

	for ref, entries := range map[string][]SkillEntry{"acme/skills:v1": acme, "globex/skills:v1": globex} {
		cat := Catalog{Ref: ref, CatalogArtifact: CatalogArtifact{Title: ref, Skills: entries}}
		dbCat, err := cat.ToDb()
		require.NoError(t, err)
		require.NoError(t, dao.UpsertCatalog(ctx, dbCat))
	}

	// Entries survive the database round trip intact.
	stored, err := dao.GetCatalog(ctx, "acme/skills:v1")
	require.NoError(t, err)
	assert.Equal(t, acme, NewFromDb(stored).Skills)

	// Same name in two publishers: both added, never deduped (D11).
	require.NoError(t, AddSkill(ctx, dao, "acme/skills:v1", "refunds", false))
	require.NoError(t, AddSkill(ctx, dao, "globex/skills:v1", "refunds", false))
	served, err := ServedSkills(ctx, dao)
	require.NoError(t, err)
	require.Len(t, served, 2)
	assert.NotEqual(t, served[0].URI, served[1].URI)

	// The catalog changes under an approval: status flips to changed and the
	// skill is no longer served until re-added.
	changed := acme
	changed[0].Resources[0].Digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	cat := Catalog{Ref: "acme/skills:v1", CatalogArtifact: CatalogArtifact{Title: "acme", Skills: changed}}
	dbCat, err := cat.ToDb()
	require.NoError(t, err)
	require.NoError(t, dao.UpsertCatalog(ctx, dbCat))

	statuses, err := AddedSkillStatuses(ctx, dao)
	require.NoError(t, err)
	byRef := map[string]string{}
	for _, st := range statuses {
		byRef[st.CatalogRef] = st.Status
	}
	assert.Equal(t, map[string]string{"acme/skills:v1": "changed", "globex/skills:v1": "ok"}, byRef)
	served, err = ServedSkills(ctx, dao)
	require.NoError(t, err)
	require.Len(t, served, 1)

	require.NoError(t, AddSkill(ctx, dao, "acme/skills:v1", "refunds", false))
	served, err = ServedSkills(ctx, dao)
	require.NoError(t, err)
	assert.Len(t, served, 2)

	// Ambiguity and removal.
	require.ErrorContains(t, RemoveSkill(ctx, dao, "refunds"), "ambiguous")
	require.NoError(t, RemoveSkill(ctx, dao, "skill://globex/refunds/SKILL.md"))
	added, err := dao.ListAddedSkills(ctx)
	require.NoError(t, err)
	require.Len(t, added, 1)

	// Dropping the catalog leaves the approval dangling as missing.
	require.NoError(t, dao.DeleteCatalog(ctx, "acme/skills:v1"))
	statuses, err = AddedSkillStatuses(ctx, dao)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "missing", statuses[0].Status)
	_ = db.AddedSkill{}
}

func TestClaudeSkillStubFollowsAddAndRemove(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	dao := setupTestDB(t)

	for dir, ref := range map[string]string{"acme": "acme/skills:v1", "globex": "globex/skills:v1"} {
		entries, _, err := LoadSkills(filepath.Join(fixtures, "publishers", dir), dir)
		require.NoError(t, err)
		dbCat, err := (Catalog{Ref: ref, CatalogArtifact: CatalogArtifact{Title: ref, Skills: entries}}).ToDb()
		require.NoError(t, err)
		require.NoError(t, dao.UpsertCatalog(ctx, dbCat))
	}

	require.NoError(t, AddSkill(ctx, dao, "acme/skills:v1", "refunds", false))
	stub := filepath.Join(home, ".claude", "skills", "refunds", "SKILL.md")
	data, err := os.ReadFile(stub)
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: refunds\n")
	assert.Contains(t, string(data), "description: Process refunds the Acme way.\n")
	assert.Contains(t, string(data), `load_skill tool with name "skill://acme/refunds/SKILL.md"`)
	assert.NotContains(t, string(data), "Acme refunds.", "the stub must not carry the skill body")

	// A colliding name from another publisher gets a qualified directory.
	require.NoError(t, AddSkill(ctx, dao, "globex/skills:v1", "refunds", false))
	_, err = os.Stat(filepath.Join(home, ".claude", "skills", "globex-refunds", "SKILL.md"))
	require.NoError(t, err)

	// Removing deletes only the matching stub; a foreign skill is untouched.
	foreign := filepath.Join(home, ".claude", "skills", "mine", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(foreign), 0o755))
	require.NoError(t, os.WriteFile(foreign, []byte("---\nname: mine\ndescription: x\n---\n"), 0o644))
	require.NoError(t, RemoveSkill(ctx, dao, "skill://acme/refunds/SKILL.md"))
	_, err = os.Stat(stub)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(foreign)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(home, ".claude", "skills", "globex-refunds", "SKILL.md"))
	require.NoError(t, err)
}

func TestClaudeSkillStubNeverOverwritesUserSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mine := filepath.Join(home, ".claude", "skills", "simple", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(mine), 0o755))
	require.NoError(t, os.WriteFile(mine, []byte("---\nname: simple\ndescription: my own\n---\nmine\n"), 0o644))

	entries, _, err := LoadSkills(filepath.Join(fixtures, "simple"), "fixtures")
	require.NoError(t, err)
	require.NoError(t, writeClaudeSkillStub(entries[0]))

	data, err := os.ReadFile(mine)
	require.NoError(t, err)
	assert.Equal(t, "---\nname: simple\ndescription: my own\n---\nmine\n", string(data), "user skill untouched")
	_, err = os.Stat(filepath.Join(home, ".claude", "skills", "fixtures-simple", "SKILL.md"))
	require.NoError(t, err, "stub written under the qualified name")
	require.NoError(t, removeClaudeSkillStub(entries[0].URI))
	_, err = os.Stat(mine)
	require.NoError(t, err)
}

func TestClaudeSkillStubSkippedWithoutClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	entries, _, err := LoadSkills(filepath.Join(fixtures, "simple"), "fixtures")
	require.NoError(t, err)
	require.NoError(t, writeClaudeSkillStub(entries[0]))
	_, err = os.Stat(filepath.Join(home, ".claude"))
	assert.True(t, os.IsNotExist(err))
}

func TestAutoAddSkillsOnPull(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	dao := setupTestDB(t)

	entries, _, err := LoadSkills(filepath.Join(fixtures, "full"), "fixtures")
	require.NoError(t, err)
	cat := Catalog{Ref: "fixtures/full:v1", CatalogArtifact: CatalogArtifact{Title: "full", Skills: entries}}
	dbCat, err := cat.ToDb()
	require.NoError(t, err)
	require.NoError(t, dao.UpsertCatalog(ctx, dbCat))

	// A pull approves everything in the catalog and writes the Claude stubs.
	require.NoError(t, autoAddSkills(ctx, dao, cat))
	served, err := ServedSkills(ctx, dao)
	require.NoError(t, err)
	assert.Len(t, served, 2)
	_, err = os.Stat(filepath.Join(home, ".claude", "skills", "helper", "SKILL.md"))
	require.NoError(t, err)

	// The next pull no longer carries the nested skill: approval and stub go away.
	cat.Skills = entries[:1]
	dbCat, err = cat.ToDb()
	require.NoError(t, err)
	require.NoError(t, dao.UpsertCatalog(ctx, dbCat))
	require.NoError(t, autoAddSkills(ctx, dao, cat))
	served, err = ServedSkills(ctx, dao)
	require.NoError(t, err)
	require.Len(t, served, 1)
	assert.Equal(t, "skill://fixtures/full/SKILL.md", served[0].URI)
	_, err = os.Stat(filepath.Join(home, ".claude", "skills", "helper"))
	assert.True(t, os.IsNotExist(err))

	// A manual opt-out holds until the next pull.
	require.NoError(t, RemoveSkill(ctx, dao, "full"))
	served, err = ServedSkills(ctx, dao)
	require.NoError(t, err)
	assert.Empty(t, served)
}
