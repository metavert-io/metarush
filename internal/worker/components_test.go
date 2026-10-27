package worker

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// A computer without FFmpeg says which component its waiting work needs,
// and takes that work on as soon as the component is added while MetaRush
// runs (D324, D326).
func TestWorkerTakesOnWhatAComponentAdds(t *testing.T) {
	bin := t.TempDir()
	t.Setenv(tools.EnvToolDirs, bin)
	a := testapp.New(t)
	w := New(a)
	if w.Can(jobs.TypeProxy) || w.Can(jobs.TypeTranscribe) {
		t.Fatalf("a bare computer takes %v", w.JobTypes())
	}
	lacks := map[string]string{}
	for _, l := range w.Lacks() {
		lacks[l.JobType] = l.Component
		if !strings.Contains(l.Says, "Settings → Components") {
			t.Errorf("%s: %q", l.JobType, l.Says)
		}
	}
	if lacks[jobs.TypeProxy] != "ffmpeg" || lacks[jobs.TypeRender] != "renderer" {
		t.Fatalf("lacks: %v", lacks)
	}
	for _, p := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(bin, p), []byte("#!/bin/sh\necho 'version 7'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a.Tools.Rescan(a.Cfg.Tools)
	w.Refresh()
	if !w.Can(jobs.TypeProxy) || !w.Can(jobs.TypeQA) {
		t.Fatalf("after FFmpeg was added: %v", w.JobTypes())
	}
	if slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.Component == "ffmpeg" }) {
		t.Fatalf("still lacks FFmpeg: %v", w.Lacks())
	}
}
