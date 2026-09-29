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
	canonicalRoot := "skills/talento"
	canonicalFiles := skillFiles(t, canonicalRoot)
	for _, copyRoot := range []string{
		"plugins/talento/skills/talento",
		"plugins/claude-code/skills/talento",
	} {
		copyFiles := skillFiles(t, copyRoot)
		if len(copyFiles) != len(canonicalFiles) {
			t.Fatalf("%s file set drifted from %s", copyRoot, canonicalRoot)
		}
		for rel, canonical := range canonicalFiles {
			copy, ok := copyFiles[rel]
			if !ok || !bytes.Equal(canonical, copy) {
				t.Fatalf("%s drifted from canonical skill", path.Join(copyRoot, rel))
			}
		}
	}
	canonical, ok := canonicalFiles["SKILL.md"]
	if !ok {
		t.Fatal("canonical skill is missing SKILL.md")
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

func skillFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := fs.WalkDir(Content, root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(Content, filePath)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(filePath, root+"/")] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
