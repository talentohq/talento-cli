package talentocli

import (
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

func TestCanonicalSkillAndGeneratedWrappersStayInSync(t *testing.T) {
	canonical, err := fs.ReadFile(Content, "skills/talento/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, copyPath := range []string{
		"plugins/talento/skills/talento/SKILL.md",
		"plugins/claude-code/skills/talento/SKILL.md",
	} {
		copy, err := fs.ReadFile(Content, copyPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical, copy) {
			t.Fatalf("%s drifted from canonical skill", copyPath)
		}
	}
	text := string(canonical)
	if !strings.HasPrefix(text, "---\n") || !strings.Contains(text, "name: talento") {
		t.Fatal("canonical skill is missing Agent Skills frontmatter")
	}
	if strings.Contains(strings.ToLower(text), "all writes are two-step") {
		t.Fatal("canonical skill contains the stale universal write claim")
	}
	core, err := fs.ReadFile(Content, "skills/talento/references/core.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(core), "submitted_for_approval") || !strings.Contains(text, "talento commands --available") {
		t.Fatal("canonical skill is missing live result/capability guidance")
	}

	referencePattern := regexp.MustCompile(`references/[a-z0-9-]+\.md`)
	for _, reference := range referencePattern.FindAllString(text, -1) {
		if _, err := fs.Stat(Content, path.Join("skills/talento", reference)); err != nil {
			t.Fatalf("missing referenced skill file %s", reference)
		}
	}
}
