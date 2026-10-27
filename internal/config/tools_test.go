package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The renderer's commands read only the tools section, so a secret that
// cannot be resolved (a locked keychain over SSH) never stops them.
func TestLoadToolsNeedsNoSecrets(t *testing.T) {
	h := t.TempDir()
	yml := "version: 1\ndatabase:\n  uri: keychain:no-such-entry\n  name: x\ntools:\n  node: /opt/node/bin/node\n  videoDir: ~/src/metarush/video\n  devRenderer: true\n"
	if err := os.WriteFile(Path(h), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_NPM", "/opt/node/lib/node_modules/npm/bin/npm-cli.js")
	tl, err := LoadTools(h)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := os.UserHomeDir()
	if tl.Node != "/opt/node/bin/node" || !tl.DevRenderer || tl.VideoDir != filepath.Join(u, "src", "metarush", "video") || tl.NPM != "/opt/node/lib/node_modules/npm/bin/npm-cli.js" {
		t.Fatalf("tools: %+v", tl)
	}
	t.Setenv("METARUSH_DEV_RENDERER", "0")
	if tl, _ := LoadTools(h); tl.DevRenderer {
		t.Fatal("METARUSH_DEV_RENDERER=0 should turn the developer renderer off")
	}
}
