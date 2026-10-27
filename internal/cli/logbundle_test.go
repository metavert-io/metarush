package cli

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// The bug-report bundle holds the log, failed jobs, the setup check, versions
// and settings — and none of the secrets that went through the log.
func TestBundleHasNoSecrets(t *testing.T) {
	const token = "bundle-env-token-0123456789abcdef"
	eleven := "sk" + "_0123456789abcdef0123456789abcdef0123456789abcdef" // put together at run time for the secret scan
	t.Setenv("SOME_SERVICE_TOKEN", token)
	a := testapp.New(t)
	homeFlag = a.Cfg.Home()
	t.Cleanup(func() { homeFlag = "" })
	ctx := context.Background()
	// The logger read the environment when the app opened; register the token as
	// the app would for a runtime secret, then push secrets through every kind of entry.
	a.Log.Secret(token)
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Film " + token}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	a.Log.ToolRun(ctx, tools.RunInfo{Tool: "ffmpeg", Args: []string{"--token", token}, ExitCode: 1, Err: errors.New("failed with " + token), Stderr: []byte("key " + eleven)})
	a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, Prompt: "my key is " + eleven, Response: "noted " + token, StartedAt: time.Now()})
	a.Log.Flush(ctx)
	path := filepath.Join(t.TempDir(), "report.zip")
	res, err := writeBundle(ctx, a, "1h", path, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entries < 3 {
		t.Fatalf("bundle entries: %+v", res)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		names[strings.TrimPrefix(f.Name, "metarush-bug-report/")] = string(b)
		for _, s := range []string{token, eleven} {
			if strings.Contains(string(b), s) {
				t.Errorf("%s contains a secret", f.Name)
			}
		}
	}
	for _, want := range []string{"README.md", "entries.jsonl", "failed-jobs.json", "doctor.json", "versions.json", "config.json"} {
		if _, ok := names[want]; !ok {
			t.Errorf("bundle is missing %s (has %v)", want, names)
		}
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(names["config.json"]), &cfg); err != nil || cfg["database"] == nil {
		t.Fatalf("config summary: %v", err)
	}
	if !strings.Contains(names["entries.jsonl"], `"kind":"claude"`) || !strings.Contains(names["README.md"], "entries.jsonl") {
		t.Fatal("bundle contents are incomplete")
	}
}
