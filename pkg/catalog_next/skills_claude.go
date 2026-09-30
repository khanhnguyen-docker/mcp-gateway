package catalognext

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/docker/mcp-gateway/pkg/skills"
	"github.com/docker/mcp-gateway/pkg/user"
)

// stubMarker in the stub's frontmatter metadata says the CLI owns the file.
const stubMarker = "mcp-gateway-skill"

// claudeSkillsDir is ~/.claude/skills, or "" when Claude Code is not set up.
func claudeSkillsDir() string {
	home, err := user.HomeDir()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills")
}

// writeClaudeSkillStub makes an approved skill a Claude Code skill: a stub that
// carries only the name and description and tells the model to load the real
// content through the gateway, where it is verified and banner-prefixed.
func writeClaudeSkillStub(entry SkillEntry) error {
	dir := claudeSkillsDir()
	if dir == "" {
		return nil
	}
	name := skills.Name(entry.URI)
	desc, _ := entry.Frontmatter["description"].(string)
	target := filepath.Join(dir, name)
	if other, err := stubURI(target); err == nil && other != entry.URI {
		// The name is taken by the user's own skill or another publisher's
		// stub: never overwrite it, qualify ours instead.
		name = skillPublisherOf(entry.URI) + "-" + name
		target = filepath.Join(dir, name)
		if other, err := stubURI(target); err == nil && other != entry.URI {
			return fmt.Errorf("%s is already a skill that is not this one; not writing a stub", target)
		}
	}
	fm, err := yaml.Marshal(map[string]any{
		"name":        name,
		"description": desc,
		"metadata":    map[string]string{stubMarker: entry.URI},
	})
	if err != nil {
		return err
	}
	body := fmt.Sprintf(`---
%s---

This skill is served by the Docker MCP Gateway (MCP server MCP_DOCKER); its
content is not stored here. Call the load_skill tool with name %q and follow
the instructions it returns. Read the files it references with read_skill_file.
`, fm, entry.URI)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, skills.SkillFile), []byte(body), 0o644)
}

// removeClaudeSkillStub deletes the stub for uri, and only a stub the CLI wrote.
func removeClaudeSkillStub(uri string) error {
	dir := claudeSkillsDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if got, err := stubURI(filepath.Join(dir, e.Name())); err == nil && got == uri {
			return os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// stubURI returns the gateway URI recorded in a stub, "" for a skill that is
// not a gateway stub, and an error when there is no SKILL.md.
func stubURI(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, skills.SkillFile))
	if err != nil {
		return "", err
	}
	fm, _, err := skills.ParseFrontmatter(data)
	if err != nil {
		return "", nil
	}
	meta, _ := fm["metadata"].(map[string]any)
	uri, _ := meta[stubMarker].(string)
	return uri, nil
}

func skillPublisherOf(uri string) string {
	pub, _, _ := strings.Cut(strings.TrimPrefix(uri, skills.Scheme), "/")
	return pub
}
