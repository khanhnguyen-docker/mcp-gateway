package catalognext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	oci "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/mcp-gateway/pkg/db"
	mcpoci "github.com/docker/mcp-gateway/pkg/oci"
	"github.com/docker/mcp-gateway/pkg/skills"
	"github.com/docker/mcp-gateway/pkg/telemetry"
	"github.com/docker/mcp-gateway/pkg/user"
)

const SkillFileMediaType = "application/vnd.docker.mcp.skill.file.v1"

// SkillEntry is a SEP-2640 entry plus the OCI layer holding each file.
type SkillEntry struct {
	skills.Entry `yaml:",inline"`
	Layers       []oci.Descriptor `json:"layers" yaml:"layers"`
}

// SkillStatus is one row of `skill ls`.
type SkillStatus struct {
	Name           string `json:"name" yaml:"name"`
	URI            string `json:"uri" yaml:"uri"`
	CatalogRef     string `json:"catalogRef" yaml:"catalogRef"`
	ManifestDigest string `json:"manifestDigest" yaml:"manifestDigest"`
	CurrentDigest  string `json:"currentDigest,omitempty" yaml:"currentDigest,omitempty"`
	Status         string `json:"status" yaml:"status"` // ok | changed | missing
}

// skillBlobDir is a variable so tests can point the store at a temp dir.
var skillBlobDir = func() (string, error) {
	home, err := user.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "mcp", "skills"), nil
}

func skillBlobPath(dgst string) (string, error) {
	dir, err := skillBlobDir()
	if err != nil {
		return "", err
	}
	algo, hex, ok := strings.Cut(dgst, ":")
	if !ok {
		return "", fmt.Errorf("invalid digest %q", dgst)
	}
	return filepath.Join(dir, algo, hex), nil
}

// LoadSkills walks a skill directory (or a directory of skills) into artifact
// entries. The layer descriptor of each file carries the same digest as its
// SEP resource.
func LoadSkills(dir, publisher string) ([]SkillEntry, []mcpoci.ExtraLayer, error) {
	walked, err := skills.WalkAll(dir, publisher)
	if err != nil {
		return nil, nil, err
	}
	var entries []SkillEntry
	var layers []mcpoci.ExtraLayer
	for _, s := range walked {
		entry := SkillEntry{Entry: s.Entry}
		for _, r := range s.Resources {
			data, err := os.ReadFile(s.Files[r.URI])
			if err != nil {
				return nil, nil, err
			}
			layer := mcpoci.ExtraLayer{
				MediaType:   SkillFileMediaType,
				Data:        data,
				Annotations: map[string]string{oci.AnnotationTitle: strings.TrimPrefix(r.URI, skills.Root(s.URI)+"/")},
			}
			entry.Layers = append(entry.Layers, layer.Descriptor())
			layers = append(layers, layer)
		}
		entries = append(entries, entry)
	}
	return entries, layers, nil
}

// PushSkills validates the directory and pushes a catalog artifact holding the
// skills to ref.
func PushSkills(ctx context.Context, dir, refStr, publisher, title string) error {
	ref, err := name.ParseReference(refStr)
	if err != nil {
		return fmt.Errorf("failed to parse reference: %w", err)
	}
	if !mcpoci.IsValidInputReference(ref) {
		return fmt.Errorf("reference must be a valid OCI reference without a digest")
	}
	if publisher == "" {
		publisher, err = defaultPublisher(ref)
		if err != nil {
			return err
		}
	}
	if title == "" {
		title = publisher + " skills"
	}
	entries, layers, err := LoadSkills(dir, publisher)
	if err != nil {
		return err
	}
	artifact := CatalogArtifact{Title: title, Skills: entries}
	hash, err := mcpoci.PushArtifactWithLayers(ctx, ref, MCPCatalogArtifactType, artifact, nil, layers)
	if err != nil {
		return fmt.Errorf("failed to push skills artifact: %w", err)
	}
	fmt.Printf("Pushed %d skill(s) to %s@sha256:%s\n", len(entries), mcpoci.FullName(ref), hash)
	return nil
}

// defaultPublisher is the repository namespace, e.g. "myorg" in "myorg/skills".
func defaultPublisher(ref name.Reference) (string, error) {
	repo := ref.Context().RepositoryStr()
	ns, _, ok := strings.Cut(repo, "/")
	if !ok || ns == "library" {
		return "", fmt.Errorf("cannot derive a publisher from %q; pass --publisher", repo)
	}
	return ns, nil
}

// pullSkillBlobs stores every skill file of the catalog in the local
// content-addressed store, verifying the bytes against the entry.
func pullSkillBlobs(ctx context.Context, ref name.Reference, entries []SkillEntry) error {
	for _, entry := range entries {
		for _, r := range entry.Resources {
			p, err := skillBlobPath(r.Digest)
			if err != nil {
				return err
			}
			if _, err := os.Stat(p); err == nil {
				continue
			}
			data, err := mcpoci.FetchBlob(ctx, ref, r.Digest)
			if err != nil {
				return fmt.Errorf("fetching %s: %w", r.URI, err)
			}
			if err := skills.Verify(r, data); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, data, 0o444); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadSkillFile returns the bytes of a listed skill file from the local store,
// rehashed against the entry on every read. Errors wrap skills.ErrUnlisted,
// skills.ErrDigest, or skills.ErrSize.
func ReadSkillFile(entry skills.Entry, uri string) ([]byte, string, error) {
	r, ok := entry.Find(uri)
	if !ok {
		return nil, "", fmt.Errorf("%w: %s is not a file of %s", skills.ErrUnlisted, uri, entry.URI)
	}
	p, err := skillBlobPath(r.Digest)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s not in local store: %w", skills.ErrDigest, uri, err)
	}
	if err := skills.Verify(r, data); err != nil {
		return nil, "", err
	}
	return data, r.Digest, nil
}

// ValidateSkills prints the entries of a directory as JSON.
func ValidateSkills(dir, publisher string) error {
	if publisher == "" {
		publisher = "local"
	}
	entries, _, err := LoadSkills(dir, publisher)
	if err != nil {
		return err
	}
	return printJSON(entries)
}

func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// findSkill resolves a name or URI within one catalog's entries. A bare name
// must be unique (D11).
func findSkill(entries []SkillEntry, nameOrURI string) (SkillEntry, error) {
	var matches []SkillEntry
	for _, e := range entries {
		if e.URI == nameOrURI || skills.Name(e.URI) == nameOrURI {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return SkillEntry{}, fmt.Errorf("skill %q not found", nameOrURI)
	case 1:
		return matches[0], nil
	}
	uris := make([]string, len(matches))
	for i, m := range matches {
		uris[i] = m.URI
	}
	return SkillEntry{}, fmt.Errorf("skill %q is ambiguous, use one of: %s", nameOrURI, strings.Join(uris, ", "))
}

func catalogSkills(ctx context.Context, dao db.DAO, refStr string) (string, []SkillEntry, error) {
	ref, err := name.ParseReference(refStr)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse reference: %w", err)
	}
	refStr = mcpoci.FullNameWithoutDigest(ref)
	dbCatalog, err := dao.GetCatalog(ctx, refStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, fmt.Errorf("catalog %s not found, pull it first", refStr)
		}
		return "", nil, err
	}
	entries, err := skillEntriesFromDb(dbCatalog.Skills)
	return refStr, entries, err
}

func skillEntriesFromDb(rows []db.CatalogSkill) ([]SkillEntry, error) {
	entries := make([]SkillEntry, len(rows))
	for i, row := range rows {
		if err := json.Unmarshal(row.Entry, &entries[i]); err != nil {
			return nil, fmt.Errorf("decoding skill %s: %w", row.URI, err)
		}
	}
	return entries, nil
}

func skillEntriesToDb(entries []SkillEntry) ([]db.CatalogSkill, error) {
	rows := make([]db.CatalogSkill, len(entries))
	for i, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		rows[i] = db.CatalogSkill{URI: e.URI, Entry: raw}
	}
	return rows, nil
}

// AddSkill records the manifest digest of a catalog skill (D4).
func AddSkill(ctx context.Context, dao db.DAO, refStr, nameOrURI string, asJSON bool) error {
	refStr, entries, err := catalogSkills(ctx, dao, refStr)
	if err != nil {
		return err
	}
	entry, err := findSkill(entries, nameOrURI)
	if err != nil {
		return err
	}
	dgst, err := skills.ManifestDigest(entry.Resources)
	if err != nil {
		return err
	}
	added := db.AddedSkill{CatalogRef: refStr, URI: entry.URI, ManifestDigest: dgst}
	if err := dao.AddSkill(ctx, added); err != nil {
		return err
	}
	telemetry.Init()
	telemetry.RecordSkillAdd(ctx, refStr)
	if err := writeClaudeSkillStub(entry); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write the Claude Code skill stub: %v\n", err)
	}
	if asJSON {
		return printJSON(SkillStatus{Name: skills.Name(entry.URI), URI: entry.URI, CatalogRef: refStr, ManifestDigest: dgst, CurrentDigest: dgst, Status: "ok"})
	}
	fmt.Printf("Added %s (%s)\n", entry.URI, dgst)
	return nil
}

// RemoveSkill removes an added skill by name or URI.
func RemoveSkill(ctx context.Context, dao db.DAO, nameOrURI string) error {
	added, err := dao.ListAddedSkills(ctx)
	if err != nil {
		return err
	}
	var matches []db.AddedSkill
	for _, a := range added {
		if a.URI == nameOrURI || skills.Name(a.URI) == nameOrURI {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("skill %q is not added", nameOrURI)
	case 1:
		if err := dao.RemoveSkill(ctx, matches[0].CatalogRef, matches[0].URI); err != nil {
			return err
		}
		return removeClaudeSkillStub(matches[0].URI)
	}
	uris := make([]string, len(matches))
	for i, m := range matches {
		uris[i] = m.URI
	}
	return fmt.Errorf("skill %q is ambiguous, use one of: %s", nameOrURI, strings.Join(uris, ", "))
}

// AddedSkillStatuses compares every added skill with the catalog's current
// entry. The gateway serves only rows with status ok.
func AddedSkillStatuses(ctx context.Context, dao db.DAO) ([]SkillStatus, error) {
	added, err := dao.ListAddedSkills(ctx)
	if err != nil {
		return nil, err
	}
	current := map[string]map[string]SkillEntry{} // catalogRef -> uri -> entry
	statuses := make([]SkillStatus, 0, len(added))
	for _, a := range added {
		if current[a.CatalogRef] == nil {
			current[a.CatalogRef] = map[string]SkillEntry{}
			if _, entries, err := catalogSkills(ctx, dao, a.CatalogRef); err == nil {
				for _, e := range entries {
					current[a.CatalogRef][e.URI] = e
				}
			}
		}
		st := SkillStatus{Name: skills.Name(a.URI), URI: a.URI, CatalogRef: a.CatalogRef, ManifestDigest: a.ManifestDigest, Status: "missing"}
		if e, ok := current[a.CatalogRef][a.URI]; ok {
			st.CurrentDigest, err = skills.ManifestDigest(e.Resources)
			if err != nil {
				return nil, err
			}
			st.Status = "changed"
			if st.CurrentDigest == a.ManifestDigest {
				st.Status = "ok"
			}
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

// ServedSkill is an added skill whose manifest still matches its catalog.
type ServedSkill struct {
	SkillEntry
	CatalogRef string
}

// ServedSkills returns the added skills whose manifest still matches the
// catalog, with their entries.
func ServedSkills(ctx context.Context, dao db.DAO) ([]ServedSkill, error) {
	statuses, err := AddedSkillStatuses(ctx, dao)
	if err != nil {
		return nil, err
	}
	var served []ServedSkill
	for _, st := range statuses {
		if st.Status != "ok" {
			continue
		}
		_, entries, err := catalogSkills(ctx, dao, st.CatalogRef)
		if err != nil {
			return nil, err
		}
		e, err := findSkill(entries, st.URI)
		if err != nil {
			return nil, err
		}
		served = append(served, ServedSkill{SkillEntry: e, CatalogRef: st.CatalogRef})
	}
	return served, nil
}

// ListSkills prints `skill ls`.
func ListSkills(ctx context.Context, dao db.DAO, asJSON bool) error {
	statuses, err := AddedSkillStatuses(ctx, dao)
	if err != nil {
		return err
	}
	if asJSON {
		if statuses == nil {
			statuses = []SkillStatus{}
		}
		return printJSON(statuses)
	}
	if len(statuses) == 0 {
		fmt.Println("No skills added. Use `docker mcp skill add <catalog-ref> <name>`.")
		return nil
	}
	fmt.Println("Name | Catalog | Status | URI")
	for _, st := range statuses {
		fmt.Printf("%s\t| %s\t| %s\t| %s\n", st.Name, st.CatalogRef, st.Status, st.URI)
	}
	return nil
}
