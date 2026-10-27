package skillsinstall

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Frontmatter reads a SKILL.md's frontmatter as YAML (as Claude Code does:
// frontmatter that isn't valid YAML is dropped whole, D319) into its keys, in
// order, and returns the body after it.
func Frontmatter(b []byte) (map[string]string, []string, []byte, error) {
	m := fmRe.FindSubmatchIndex(b)
	if m == nil {
		return nil, nil, nil, fmt.Errorf("no frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b[m[2]:m[3]], &doc); err != nil {
		return nil, nil, nil, fmt.Errorf("the frontmatter isn't valid YAML (Claude Code would drop all of it): %v", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, nil, fmt.Errorf("the frontmatter isn't a list of keys")
	}
	keys := map[string]string{}
	var order []string
	mp := doc.Content[0].Content
	for i := 0; i+1 < len(mp); i += 2 {
		if mp[i+1].Kind != yaml.ScalarNode {
			return nil, nil, nil, fmt.Errorf("frontmatter key %s is not one line of text", mp[i].Value)
		}
		keys[mp[i].Value] = mp[i+1].Value
		order = append(order, mp[i].Value)
	}
	return keys, order, b[m[1]:], nil
}

var fmRe = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n`)
