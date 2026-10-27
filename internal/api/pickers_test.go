package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/tools"
)

// The folder picker and "Open in Finder" act on this computer's screen, so
// the signed-in dashboard of this computer reaches them through narrow
// endpoints (stand-ins for the picker and the opener here): the folder
// chosen, or a picker closed without one; the new media's folder, the
// watched folder (with how many files are in it), a linked folder; a folder
// opened. A computer with no picker says so plainly.
func TestFolderPickersOnThisComputer(t *testing.T) {
	d, _, _ := newDash(t, noClaude)
	d.signIn()
	if err := config.Save(d.a.Cfg.Home(), d.a.Cfg); err != nil { // this computer's settings, as setup leaves them
		t.Fatal(err)
	}
	picked := filepath.Join(t.TempDir(), "Studio footage")
	if err := os.MkdirAll(picked, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"take-1.mov", "take-2.mov"} {
		_ = os.WriteFile(filepath.Join(picked, n), []byte("not really a video"), 0o600)
	}
	// The picker prints the folder a person chose, nothing when they close
	// it, or fails; the opener notes what it opened.
	bin := t.TempDir()
	opened := filepath.Join(t.TempDir(), "opened")
	t.Setenv("MR_TEST_PICKED", picked)
	t.Setenv("MR_TEST_OPENED", opened)
	t.Setenv("MR_TEST_PICKER_FAILS", "")
	picker := filepath.Join(bin, "osascript")
	_ = os.WriteFile(picker, []byte("#!/bin/sh\n[ -n \"$MR_TEST_PICKER_FAILS\" ] && exit 1\nprintf '%s\\n' \"$MR_TEST_PICKED\"\n"), 0o755)
	opener := filepath.Join(bin, "open")
	_ = os.WriteFile(opener, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$MR_TEST_OPENED\"\n"), 0o755)
	d.a.Tools.Use(tools.OSAScript, picker)
	d.a.Tools.Use(tools.Open, opener)
	d.a.Tools.Use(tools.XDGOpen, opener)
	mac := runtime.GOOS == "darwin"
	unsupported := func(what string, code int, env envelope) {
		t.Helper()
		if code == 200 || env.Error.Details["reason"] != "unsupported" {
			t.Fatalf("%s with no folder picker: %d %+v", what, code, env.Error)
		}
	}

	// The folder for the media.
	code, env := d.do("POST", "/api/storage/choose", map[string]any{}, true)
	var st StorageChooseOut
	_ = json.Unmarshal(env.Data, &st)
	switch {
	case !mac:
		unsupported("the media's folder", code, env)
	case code != 200 || st.Path != picked:
		t.Fatalf("the media's folder: %d %+v %+v", code, st, env.Error)
	}

	// The watched folder: where it is, in words, and what is in it already.
	code, env = d.do("POST", "/api/watch/choose", ingest.WatchChooseIn{}, true)
	var wc ingest.WatchChooseOut
	_ = json.Unmarshal(env.Data, &wc)
	switch {
	case !mac:
		unsupported("the watched folder", code, env)
	case code != 200 || wc.Path != picked || wc.Files != 2 || wc.Display == "":
		t.Fatalf("the watched folder: %d %+v %+v", code, wc, env.Error)
	}
	if code, env := d.do("POST", "/api/watch/choose", "not a request", true); code != http.StatusBadRequest {
		t.Fatalf("a request that isn't one: %d %+v", code, env.Error)
	}

	// A folder linked to MetaRush, then changed on this computer.
	code, env = d.do("POST", "/api/resources/folders/choose", FolderChooseIn{}, true)
	var fc FolderChooseOut
	_ = json.Unmarshal(env.Data, &fc)
	switch {
	case !mac:
		unsupported("a linked folder", code, env)
	case code != 200 || fc.Linked == nil || fc.Linked.Folder.ID == "":
		t.Fatalf("a linked folder: %d %+v %+v", code, fc, env.Error)
	default:
		if code, env := d.do("POST", "/api/resources/folders/set", map[string]any{"folder": fc.Linked.Folder.ID, "pause": true}, true); code != 200 {
			t.Fatalf("pausing the linked folder: %d %+v", code, env.Error)
		}
		// Choosing it again from the picker moves where it is on this computer.
		if code, env := d.do("POST", "/api/resources/folders/choose", FolderChooseIn{FolderID: fc.Linked.Folder.ID}, true); code != 200 {
			t.Fatalf("the linked folder's place on this computer: %d %+v", code, env.Error)
		}
	}
	// Where a linked folder is comes only from the picker.
	if code, env := d.do("POST", "/api/resources/folders/set", map[string]any{"folder": "fld_x", "path": "/etc"}, true); code != http.StatusBadRequest ||
		env.Error.Message != "Choose the folder with the folder picker." {
		t.Fatalf("a folder typed in: %d %+v", code, env.Error)
	}
	if code, _ := d.raw("POST", "/api/resources/folders/set", []byte("{")); code != http.StatusBadRequest {
		t.Fatalf("a request cut short: %d", code)
	}
	if code, _ := d.raw("POST", "/api/resources/folders/choose", []byte("{")); code != http.StatusBadRequest {
		t.Fatalf("a choice cut short: %d", code)
	}

	// Closed without a folder: nothing changes.
	t.Setenv("MR_TEST_PICKED", "")
	if mac {
		for _, path := range []string{"/api/storage/choose", "/api/watch/choose", "/api/resources/folders/choose"} {
			code, env := d.do("POST", path, map[string]any{}, true)
			var out struct {
				Canceled bool `json:"canceled"`
			}
			_ = json.Unmarshal(env.Data, &out)
			if code != 200 || !out.Canceled {
				t.Fatalf("%s closed without a folder: %d %+v", path, code, env.Error)
			}
		}
		// A picker that doesn't open says so.
		t.Setenv("MR_TEST_PICKER_FAILS", "1")
		for _, path := range []string{"/api/storage/choose", "/api/watch/choose", "/api/resources/folders/choose"} {
			if code, env := d.do("POST", path, map[string]any{}, true); code != http.StatusInternalServerError || !strings.Contains(env.Error.Message, "folder picker didn't open") {
				t.Fatalf("%s when the picker fails: %d %+v", path, code, env.Error)
			}
		}
	}

	// Opening a folder on this computer's screen.
	code, env = d.do("POST", "/api/watch/reveal", ingest.WatchRevealIn{Dir: picked}, true)
	var rv ingest.WatchRevealOut
	_ = json.Unmarshal(env.Data, &rv)
	if got, _ := os.ReadFile(opened); code != 200 || rv.Opened == "" || string(got) != picked {
		t.Fatalf("opening the folder: %d %+v %+v (opened %q)", code, rv, env.Error, got)
	}
	if code, env := d.do("POST", "/api/watch/reveal", ingest.WatchRevealIn{Dir: filepath.Join(picked, "gone")}, true); code != http.StatusBadRequest ||
		env.Error.Message != "There's no folder to open." {
		t.Fatalf("a folder that isn't there: %d %+v", code, env.Error)
	}
	if code, _ := d.raw("POST", "/api/watch/reveal", []byte("{")); code != http.StatusBadRequest {
		t.Fatalf("a request cut short: %d", code)
	}
}
