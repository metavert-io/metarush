package abilities

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestPublishingNotYetRunnable keeps the publishing abilities MetaRush can't
// run yet honest (D307): each says so in its first line, the check refuses it
// for exactly the things its notes.md says are missing, and the rest of the
// manifest is sound. When MetaRush grows one of them, this fails and says to
// move the ability on.
func TestPublishingNotYetRunnable(t *testing.T) {
	cases := []struct {
		name    string
		missing []string // manifest lines MetaRush can't read yet
		kinds   []string // inputs whose kinds MetaRush doesn't take yet
		program string
	}{
		{"vimeo-upload", nil, []string{"video", "notes"}, "upload.py"},
		{"linkedin-video", []string{"signin:"}, []string{"video", "notes"}, "post.py"},
		{"podcast-episode", nil, []string{"episode", "notes"}, "post.py"},
	}
	for _, c := range cases {
		dir := filepath.Join("..", "..", "abilities", c.name)
		b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "# NOT YET RUNNABLE.") {
			t.Errorf("%s's manifest says it isn't runnable yet in its first line", c.name)
		}
		if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
			t.Errorf("%s has no notes.md saying what is missing", c.name)
		}
		_, err = ParseManifest(b)
		if len(c.missing) > 0 && (err == nil || !strings.Contains(err.Error(), strings.TrimSuffix(c.missing[0], ":")+" isn't a field a manifest has")) {
			t.Fatalf("%s: the reader refuses what MetaRush can't read yet: %v", c.name, err)
		}
		var kept []string
		for _, line := range strings.Split(string(b), "\n") {
			skip := false
			for _, m := range c.missing {
				skip = skip || strings.HasPrefix(line, m)
			}
			if !skip {
				kept = append(kept, line)
			}
		}
		m, err := ParseManifest([]byte(strings.Join(kept, "\n")))
		if err != nil {
			t.Fatalf("%s without %v: %v", c.name, c.missing, err)
		}
		// What Validate still says: only the input kinds MetaRush doesn't
		// take yet (and, as manifests only, the program isn't written).
		var said []string
		for _, p := range m.Validate(map[string]bool{ManifestFile: true, "notes.md": true}) {
			said = append(said, p.Field+": "+p.Text)
		}
		sort.Strings(said)
		var unexpected []string
		for _, s := range said {
			ok := strings.Contains(s, c.program)
			for _, k := range c.kinds {
				ok = ok || strings.HasPrefix(s, "inputs."+k+": kind is one of")
			}
			if !ok {
				unexpected = append(unexpected, s)
			}
		}
		if len(unexpected) > 0 {
			t.Errorf("%s: the manifest is unsound beyond what is missing: %q", c.name, unexpected)
		}
		for _, k := range c.kinds {
			if !strings.Contains(strings.Join(said, "\n"), "inputs."+k+": kind is one of") {
				t.Errorf("%s: MetaRush now takes the input kind of %s: move the ability on (D307)", c.name, k)
			}
		}
		if !bool(m.ChangesOutside) {
			t.Errorf("%s: posting is a person's step every run (changes-outside: yes)", c.name)
		}
	}
}
