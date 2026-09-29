package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalkSimple(t *testing.T) {
	skills, err := Walk("testdata/simple", "fixtures")
	require.NoError(t, err)
	require.Len(t, skills, 1)
	s := skills[0]
	assert.Equal(t, "skill://fixtures/simple/SKILL.md", s.URI)
	assert.Equal(t, "simple", s.Frontmatter["name"])
	assert.Equal(t, "simple", Name(s.URI))
	require.Len(t, s.Resources, 1)

	data, err := os.ReadFile("testdata/simple/SKILL.md")
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	assert.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), s.Resources[0].Digest)
	assert.Equal(t, int64(len(data)), s.Resources[0].Size)
	assert.Equal(t, "testdata/simple/SKILL.md", s.Files[s.URI])
}

func TestWalkNested(t *testing.T) {
	skills, err := Walk("testdata/full", "fixtures")
	require.NoError(t, err)
	require.Len(t, skills, 2, "enclosing skill and nested skill")

	full, helper := skills[0], skills[1]
	assert.Equal(t, "skill://fixtures/full/SKILL.md", full.URI)
	assert.Equal(t, "skill://fixtures/full/subskills/helper/SKILL.md", helper.URI)
	assert.Equal(t, "helper", Name(helper.URI))

	// Frontmatter passes through verbatim, allowed-tools included.
	assert.Equal(t, "MIT", full.Frontmatter["license"])
	assert.Equal(t, "Bash(git:*) Read", full.Frontmatter["allowed-tools"])
	assert.Equal(t, map[string]any{"author": "fixtures", "version": "1.0"}, full.Frontmatter["metadata"])

	// The nested SKILL.md is a supporting file of the enclosing skill (D9).
	_, ok := full.Find("skill://fixtures/full/subskills/helper/SKILL.md")
	assert.True(t, ok)
	_, ok = full.Find("skill://fixtures/full/templates/regional/eu-invoice.md")
	assert.True(t, ok)
	assert.Len(t, full.Resources, 7)

	// The nested skill only sees its own files, relative to its own root.
	assert.Len(t, helper.Resources, 2)
	_, ok = helper.Find("skill://fixtures/full/subskills/helper/notes.md")
	assert.True(t, ok)

	assert.NotEqual(t, full.ManifestDigest, helper.ManifestDigest)
}

func TestWalkAllTwoPublishersSameName(t *testing.T) {
	acme, err := WalkAll("testdata/publishers/acme", "acme")
	require.NoError(t, err)
	globex, err := WalkAll("testdata/publishers/globex", "globex")
	require.NoError(t, err)
	require.Len(t, acme, 1)
	require.Len(t, globex, 1)
	assert.Equal(t, Name(acme[0].URI), Name(globex[0].URI))
	assert.NotEqual(t, acme[0].URI, globex[0].URI)
	assert.NotEqual(t, acme[0].ManifestDigest, globex[0].ManifestDigest)
}

func TestWalkErrors(t *testing.T) {
	_, err := Walk("testdata", "fixtures")
	require.ErrorIs(t, err, ErrNoSkillFile)
	_, err = Walk("testdata/simple", "bad publisher")
	require.ErrorIs(t, err, ErrPublisher)

	dir := filepath.Join(t.TempDir(), "renamed")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	src, _ := os.ReadFile("testdata/simple/SKILL.md")
	require.NoError(t, os.WriteFile(filepath.Join(dir, SkillFile), src, 0o644))
	_, err = Walk(dir, "fixtures")
	require.ErrorIs(t, err, ErrName, "name must match directory")
}

func TestLimits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "big")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: big\ndescription: big\n---\n"), 0o644))
	for i := range MaxResources {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), []byte("x"), 0o644))
	}
	_, err := Walk(dir, "fixtures")
	require.ErrorIs(t, err, ErrTooManyResources)

	dir = filepath.Join(t.TempDir(), "huge")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, SkillFile), []byte("---\nname: huge\ndescription: huge\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "blob"), make([]byte, MaxTotalSize), 0o644))
	_, err = Walk(dir, "fixtures")
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestParseAndValidate(t *testing.T) {
	_, _, err := ParseFrontmatter([]byte("# no frontmatter"))
	require.ErrorIs(t, err, ErrFrontmatter)
	_, _, err = ParseFrontmatter([]byte("---\nname: x\n"))
	require.ErrorIs(t, err, ErrFrontmatter)

	fm, body, err := ParseFrontmatter([]byte("---\nname: x\ndescription: y\n---\nbody\n"))
	require.NoError(t, err)
	assert.Equal(t, "body\n", string(body))
	require.NoError(t, Validate(fm, "x"))

	for name, fm := range map[string]map[string]any{
		"upper":    {"name": "X", "description": "y"},
		"hyphens":  {"name": "a--b", "description": "y"},
		"leading":  {"name": "-a", "description": "y"},
		"missing":  {"description": "y"},
		"too long": {"name": string(make([]byte, 65)), "description": "y"},
	} {
		dirName, _ := fm["name"].(string)
		require.ErrorIs(t, Validate(fm, dirName), ErrName, name)
	}
	require.ErrorIs(t, Validate(map[string]any{"name": "x"}, "x"), ErrDescription)
	require.ErrorIs(t, Validate(map[string]any{"name": "x", "description": "y", "compatibility": ""}, "x"), ErrCompatibility)
}

func TestManifestDigestIsOrderIndependent(t *testing.T) {
	a := Resource{URI: "skill://p/s/SKILL.md", Digest: "sha256:aa", Size: 1}
	b := Resource{URI: "skill://p/s/b.md", Digest: "sha256:bb", Size: 2}
	d1, err := ManifestDigest([]Resource{a, b})
	require.NoError(t, err)
	d2, err := ManifestDigest([]Resource{b, a})
	require.NoError(t, err)
	assert.Equal(t, d1, d2)
	// Hand-computed RFC 8785 form: keys sorted, no whitespace, sorted by uri.
	canonical := `[{"digest":"sha256:aa","size":1,"uri":"skill://p/s/SKILL.md"},{"digest":"sha256:bb","size":2,"uri":"skill://p/s/b.md"}]`
	assert.Equal(t, Digest([]byte(canonical)), d1)
}

func TestVerify(t *testing.T) {
	data := []byte("hello")
	r := Resource{URI: "skill://p/s/SKILL.md", Digest: Digest(data), Size: 5}
	require.NoError(t, Verify(r, data))
	require.ErrorIs(t, Verify(r, []byte("hello!")), ErrSize)
	require.ErrorIs(t, Verify(r, []byte("hellO")), ErrDigest)
}

func TestResolve(t *testing.T) {
	assert.Equal(t, "skill://acme/billing/refunds/references/GUIDE.md",
		Resolve("skill://acme/billing/refunds/SKILL.md", "references/GUIDE.md"))
	assert.Equal(t, "skill://fixtures/full/subskills/helper/notes.md",
		Resolve("skill://fixtures/full/subskills/helper/SKILL.md", "./notes.md"))
}
