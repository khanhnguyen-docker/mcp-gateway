// Package skills implements the Agent Skills directory format and the
// SEP-2640 entry shape. It is copied verbatim into other repositories, so it
// imports only the standard library, yaml, and the RFC 8785 canonicalizer.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"gopkg.in/yaml.v3"
)

const (
	Scheme    = "skill://"
	SkillFile = "SKILL.md"

	// SEP-2640 per-skill limits.
	MaxResources = 512
	MaxTotalSize = 16 * 1024 * 1024
)

var (
	ErrNoSkillFile      = errors.New("SKILL.md not found")
	ErrFrontmatter      = errors.New("invalid frontmatter")
	ErrName             = errors.New("invalid name")
	ErrDescription      = errors.New("invalid description")
	ErrCompatibility    = errors.New("invalid compatibility")
	ErrPublisher        = errors.New("invalid publisher")
	ErrNotRegularFile   = errors.New("not a regular file")
	ErrTooManyResources = fmt.Errorf("more than %d resources", MaxResources)
	ErrTooLarge         = fmt.Errorf("total size exceeds %d bytes", MaxTotalSize)

	// Verification failure classes, see Verify.
	ErrUnlisted = errors.New("unlisted")
	ErrDigest   = errors.New("digest")
	ErrSize     = errors.New("size")
)

var (
	nameRe      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	publisherRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
)

// Resource is one file of a skill, as listed in a SEP-2640 entry.
type Resource struct {
	URI    string `json:"uri" yaml:"uri"`
	Digest string `json:"digest" yaml:"digest"`
	Size   int64  `json:"size" yaml:"size"`
}

// Entry is a SEP-2640 skill entry plus the manifest digest approvals bind to.
type Entry struct {
	URI            string         `json:"uri" yaml:"uri"`
	Frontmatter    map[string]any `json:"frontmatter" yaml:"frontmatter"`
	Resources      []Resource     `json:"resources" yaml:"resources"`
	ManifestDigest string         `json:"manifestDigest" yaml:"manifestDigest"`
}

// Skill is an Entry together with the local path of every file, for pushing.
type Skill struct {
	Entry
	Files map[string]string `json:"-" yaml:"-"` // uri -> local path
}

// Name returns the skill name encoded in a SKILL.md URI.
func Name(uri string) string {
	root := Root(uri)
	return root[strings.LastIndex(root, "/")+1:]
}

// Root returns the skill directory URI for any file URI inside the skill,
// given the SKILL.md URI. For skill://a/b/SKILL.md it is skill://a/b.
func Root(skillURI string) string {
	return strings.TrimSuffix(skillURI, "/"+SkillFile)
}

// ParseFrontmatter splits SKILL.md into its YAML frontmatter and body.
func ParseFrontmatter(content []byte) (map[string]any, []byte, error) {
	const fence = "---"
	rest, ok := bytes.CutPrefix(content, []byte(fence+"\n"))
	if !ok {
		return nil, nil, fmt.Errorf("%w: file does not start with ---", ErrFrontmatter)
	}
	head, body, found := bytes.Cut(rest, []byte("\n"+fence))
	if !found {
		return nil, nil, fmt.Errorf("%w: closing --- not found", ErrFrontmatter)
	}
	var fm map[string]any
	if err := yaml.Unmarshal(head, &fm); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrFrontmatter, err)
	}
	if fm == nil {
		return nil, nil, fmt.Errorf("%w: empty", ErrFrontmatter)
	}
	return fm, bytes.TrimPrefix(body, []byte("\n")), nil
}

// Validate checks the frontmatter per agentskills.io. dirName is the
// directory holding SKILL.md; name must equal it. Other fields pass through.
func Validate(fm map[string]any, dirName string) error {
	name, _ := fm["name"].(string)
	switch {
	case name == "" || len(name) > 64 || !nameRe.MatchString(name):
		return fmt.Errorf("%w: %q must be 1-64 lowercase alphanumerics and single hyphens", ErrName, name)
	case name != dirName:
		return fmt.Errorf("%w: %q does not match directory %q", ErrName, name, dirName)
	}
	desc, _ := fm["description"].(string)
	if desc == "" || len(desc) > 1024 {
		return fmt.Errorf("%w: must be 1-1024 characters", ErrDescription)
	}
	if c, ok := fm["compatibility"]; ok {
		s, isStr := c.(string)
		if !isStr || s == "" || len(s) > 500 {
			return fmt.Errorf("%w: must be a string of 1-500 characters", ErrCompatibility)
		}
	}
	return nil
}

// Walk reads the skill at dir and returns it plus every nested skill, each
// with complete resources. Nested SKILL.md files are supporting files of the
// enclosing skill and entries of their own.
func Walk(dir, publisher string) ([]Skill, error) {
	if !publisherRe.MatchString(publisher) {
		return nil, fmt.Errorf("%w: %q", ErrPublisher, publisher)
	}
	dir = filepath.Clean(dir)
	if _, err := os.Stat(filepath.Join(dir, SkillFile)); err != nil {
		return nil, fmt.Errorf("%w in %s", ErrNoSkillFile, dir)
	}

	// Every file once, then each skill directory takes the files under it.
	type file struct{ rel, abs string }
	var files []file
	var skillDirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if _, err := os.Stat(filepath.Join(p, SkillFile)); err == nil {
				skillDirs = append(skillDirs, rel)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s", ErrNotRegularFile, rel)
		}
		files = append(files, file{rel: rel, abs: p})
		return nil
	})
	if err != nil {
		return nil, err
	}

	digests := map[string]Resource{} // rel -> digest/size, computed once
	for _, f := range files {
		data, err := os.ReadFile(f.abs)
		if err != nil {
			return nil, err
		}
		digests[f.rel] = Resource{Digest: Digest(data), Size: int64(len(data))}
	}

	var skills []Skill
	for _, sd := range skillDirs {
		skillDir := dir
		prefix := ""
		if sd != "." {
			skillDir = filepath.Join(dir, filepath.FromSlash(sd))
			prefix = sd + "/"
		}
		content, err := os.ReadFile(filepath.Join(skillDir, SkillFile))
		if err != nil {
			return nil, err
		}
		fm, _, err := ParseFrontmatter(content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", prefix+SkillFile, err)
		}
		if err := Validate(fm, filepath.Base(skillDir)); err != nil {
			return nil, fmt.Errorf("%s: %w", prefix+SkillFile, err)
		}
		root := Scheme + publisher + "/" + filepath.Base(dir)
		if sd != "." {
			root += "/" + sd
		}
		s := Skill{Entry: Entry{URI: root + "/" + SkillFile, Frontmatter: fm}, Files: map[string]string{}}
		var total int64
		for _, f := range files {
			if !strings.HasPrefix(f.rel, prefix) {
				continue
			}
			r := digests[f.rel]
			r.URI = root + "/" + strings.TrimPrefix(f.rel, prefix)
			s.Resources = append(s.Resources, r)
			s.Files[r.URI] = f.abs
			total += r.Size
		}
		if len(s.Resources) > MaxResources {
			return nil, fmt.Errorf("%s: %w", s.URI, ErrTooManyResources)
		}
		if total > MaxTotalSize {
			return nil, fmt.Errorf("%s: %w", s.URI, ErrTooLarge)
		}
		s.ManifestDigest, err = ManifestDigest(s.Resources)
		if err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}
	return skills, nil
}

// WalkAll accepts a skill directory or a directory whose children are skills.
func WalkAll(dir, publisher string) ([]Skill, error) {
	if _, err := os.Stat(filepath.Join(dir, SkillFile)); err == nil {
		return Walk(dir, publisher)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(child, SkillFile)); err != nil {
			continue
		}
		s, err := Walk(child, publisher)
		if err != nil {
			return nil, err
		}
		skills = append(skills, s...)
	}
	if len(skills) == 0 {
		return nil, fmt.Errorf("%w in %s or its children", ErrNoSkillFile, dir)
	}
	return skills, nil
}

// Digest is the SEP-2640 digest of raw bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ManifestDigest is sha256 over the RFC 8785 canonical JSON of the resources
// sorted by URI. Approvals bind to it.
func ManifestDigest(resources []Resource) (string, error) {
	sorted := slices.Clone(resources)
	slices.SortFunc(sorted, func(a, b Resource) int { return strings.Compare(a.URI, b.URI) })
	raw, err := json.Marshal(sorted)
	if err != nil {
		return "", err
	}
	canonical, err := jsoncanonicalizer.Transform(raw)
	if err != nil {
		return "", err
	}
	return Digest(canonical), nil
}

// Find returns the resource listed for uri in the entry.
func (e Entry) Find(uri string) (Resource, bool) {
	for _, r := range e.Resources {
		if r.URI == uri {
			return r, true
		}
	}
	return Resource{}, false
}

// Verify checks data against the listed resource. The error wraps ErrSize or
// ErrDigest so callers can name the class.
func Verify(r Resource, data []byte) error {
	if int64(len(data)) != r.Size {
		return fmt.Errorf("%w: %s has %d bytes, manifest lists %d", ErrSize, r.URI, len(data), r.Size)
	}
	if d := Digest(data); d != r.Digest {
		return fmt.Errorf("%w: %s is %s, manifest lists %s", ErrDigest, r.URI, d, r.Digest)
	}
	return nil
}

// Resolve resolves a relative reference from SKILL.md against the skill root.
func Resolve(skillURI, ref string) string {
	root := Root(skillURI)
	return Scheme + path.Join(strings.TrimPrefix(root, Scheme), ref)
}
