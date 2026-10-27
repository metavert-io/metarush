package skillsinstall

import (
	"strings"
	"testing"
)

const yamlSkill = `---
name: video-sample
description: Do the sample thing with the person's video — a router.
when_to_use: '"what''s the sample", "make a sample"'
---

# Sample
`

// Frontmatter that isn't valid YAML is refused: Claude Code drops all of it,
// so the skill would load without its description. This is how MetaRush's
// skills wrote when_to_use until 0.14 (D319).
func TestFrontmatterMustBeYAML(t *testing.T) {
	bad := strings.Replace(yamlSkill, `'"what''s the sample", "make a sample"'`, `"what's the sample", "make a sample"`, 1)
	if _, _, _, err := Frontmatter([]byte(bad)); err == nil || !strings.Contains(err.Error(), "YAML") {
		t.Fatalf("got %v", err)
	}
	keys, order, _, err := Frontmatter([]byte(yamlSkill))
	if err != nil || keys["when_to_use"] != `"what's the sample", "make a sample"` || len(order) != 3 {
		t.Fatalf("got %v %v %v", keys, order, err)
	}
}
