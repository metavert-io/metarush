package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/logbook/logops"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// maxBundleEntries caps the entries one bug report carries.
const maxBundleEntries = 20000

// BundleOut describes a bug-report bundle.
type BundleOut struct {
	File        string    `json:"file"`
	Since       time.Time `json:"since"`
	Entries     int       `json:"entries"`
	FailedJobs  int       `json:"failedJobs"`
	Problems    int       `json:"problems" jsonschema:"description=Entries that are errors or warnings."`
	WithPrompts bool      `json:"withPrompts"`
	Files       []string  `json:"files"`
}

func logBundleCmd() *cobra.Command {
	var since, out string
	var noPrompts bool
	c := &cobra.Command{Use: "bundle", Short: "Make a bug report: a zip of what MetaRush did lately, what failed, its setup check and settings (no passwords or keys)",
		Long: "Collects what someone helping you (or a Claude Code session) needs to understand a problem: the log of the last\n" +
			"hours, the jobs that failed with what went wrong, the setup check, program versions and a summary of the\n" +
			"settings. Passwords and keys are removed, and the file is checked for them before it is saved.",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			path := out
			if path == "" {
				path = defaultBundlePath(time.Now())
			}
			if err := logbook.CheckOutput(path); err != nil {
				return failErr(c, err)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			res, err := writeBundle(ctx, a, since, path, !noPrompts)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, res, func() { printBundle(res, detailsMode(c)) })
		}}
	c.Flags().StringVar(&since, "since", "2h", "how far back: a duration (2h, 1d) or a date")
	c.Flags().StringVar(&out, "out", "", "the zip file to write (default: your Downloads folder)")
	c.Flags().BoolVar(&noPrompts, "no-prompts", false, "leave out what was asked of Claude and its answers")
	return c
}

// printBundle says what a bug report holds and where it is, by folder name
// (the full path with --details).
func printBundle(res *BundleOut, details bool) {
	fmt.Printf("Saved a bug report with %s and %s from %s, %s.\n",
		logEntries(res.Entries), plural(res.FailedJobs, "failed job"), res.Since.Local().Format("Mon 2 Jan 15:04"), whereSaved(res.File))
	fmt.Println("Passwords and keys were removed. Share the file with whoever helps you, or ask Claude Code to read it.")
	if details {
		fmt.Println("file: " + res.File)
	}
}

// whereSaved names where a file went the way people say it: "in your
// Downloads folder (name.zip)"; elsewhere just its name.
func whereSaved(path string) string {
	dir, name := filepath.Dir(path), filepath.Base(path)
	if h, err := os.UserHomeDir(); err == nil {
		switch dir {
		case filepath.Join(h, "Downloads"):
			return "in your Downloads folder (" + name + ")"
		case filepath.Join(h, "Desktop"):
			return "on your Desktop (" + name + ")"
		case h:
			return "in your home folder (" + name + ")"
		}
	}
	if dir == filepath.Clean(os.TempDir()) {
		return "in the temporary folder (" + name + ")"
	}
	return "as " + name
}

func defaultBundlePath(now time.Time) string {
	name := "metarush-bug-report-" + now.Format("20060102-150405") + ".zip"
	if h, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{filepath.Join(h, "Downloads"), filepath.Join(h, "Desktop"), h} {
			if st, err := os.Stat(d); err == nil && st.IsDir() && logbook.CheckOutput(filepath.Join(d, name)) == nil {
				return filepath.Join(d, name)
			}
		}
	}
	return filepath.Join(os.TempDir(), name)
}

// failedJob is one failed job in a bundle.
type failedJob struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Title    string    `json:"title"`
	Error    string    `json:"error"`
	Advice   string    `json:"advice,omitempty"`
	Machine  string    `json:"machine,omitempty"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
	Stderr   []string  `json:"stderr,omitempty" jsonschema:"description=The end of the error output of the programs it ran that failed."`
}

const bundleReadme = `# MetaRush bug report

Made by ` + "`metarush log bundle`" + ` on %s, covering %s to %s.
Secrets (database passwords, storage keys, API keys, tokens, cookies) were removed before anything was written,
and the files were checked for them afterwards.

| File | What it holds |
|---|---|
| entries.jsonl | The activity log for the period, newest first: one JSON object per line. ` + "`kind`" + ` is op (an operation call), op_summary (dashboard reads, counted), job (a job event), tool (a program run: arguments, exit status, error output when it failed), claude (a Claude Code turn: prompt, final reply, tool calls, action cards), action (a person's decision on a change Claude asked for) or event. ` + "`level`" + ` error/warn marks problems; ` + "`refs`" + ` link entries to projects, clips, jobs, renders and conversations; ` + "`parentId`" + ` links a program run to the operation that caused it. %s |
| failed-jobs.json | Jobs that failed in the period: what they were, the error, the advice MetaRush gives, and the end of the error output of the programs they ran. |
| doctor.json | The setup check (` + "`metarush doctor --json`" + `): tools, database, media store, security settings. |
| versions.json | MetaRush, Go, operating system, program versions and the database schema version. |
| config.json | A summary of this installation's settings, without secrets. |

To investigate: start with failed-jobs.json and the entries with ` + "`\"level\":\"error\"`" + `, then follow their ` + "`parentId`" + ` and ` + "`refs`" + `.
`

func writeBundle(ctx context.Context, a *app.App, since, path string, withPrompts bool) (*BundleOut, error) {
	now := time.Now().UTC()
	from, err := logbook.ParseWhen(since, now)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	res := &BundleOut{File: path, Since: from, WithPrompts: withPrompts}
	var entries bytes.Buffer
	var all []logops.ExportEntry
	n, err := exportEntries(ctx, a, logops.LogListIn{Since: from.Format(time.RFC3339Nano)}, withPrompts, maxBundleEntries, func(e logops.ExportEntry) error {
		if !withPrompts && e.Claude != nil {
			e.Claude.Prompt, e.Claude.Response, e.Claude.PromptRef, e.Claude.ResponseRef = "", "", nil, nil
			if e.Bodies != nil {
				e.Bodies.Prompt, e.Bodies.Response = "", ""
			}
		}
		if e.Level == logbook.LevelError || e.Level == logbook.LevelWarn {
			res.Problems++
		}
		all = append(all, e)
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		entries.Write(append(b, '\n'))
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Entries = n
	// Failed jobs with advice and the error output of their failed program runs.
	jobs := []failedJob{}
	list, err := ops.CallTyped[ops.JobListOut](ctx, a, "job.list", ops.JobListIn{State: models.JobFailed}, ops.Caller{})
	if err == nil {
		for _, j := range list.Jobs {
			at := j.UpdatedAt
			if j.FinishedAt != nil {
				at = *j.FinishedAt
			}
			if at.Before(from) || len(jobs) >= 50 {
				continue
			}
			fj := failedJob{ID: j.ID, Type: j.Type, Title: ops.JobKindLabel(j.Type), Error: j.Error, Attempts: j.AttemptCount, At: at}
			if d, err := ops.JobDetail(ctx, a, j.ID); err == nil {
				fj.Title, fj.Advice, fj.Machine = d.Title, d.Advice, d.Machine
			}
			for _, e := range all {
				if e.Kind == logbook.KindTool && e.Refs.JobID == j.ID && e.Tool != nil && !e.Tool.OK {
					s := e.Tool.StderrTail
					if e.Bodies != nil && e.Bodies.Stderr != "" {
						s = e.Bodies.Stderr
					}
					if s != "" {
						fj.Stderr = append(fj.Stderr, e.Tool.Program+": "+s)
					}
				}
			}
			jobs = append(jobs, fj)
		}
	}
	res.FailedJobs = len(jobs)
	versions := bundleVersions(ctx, a)
	files := []struct {
		name string
		data any
	}{
		{"failed-jobs.json", jobs},
		{"doctor.json", map[string]any{"checks": runDoctor(ctx)}},
		{"versions.json", versions},
		{"config.json", configSummary(a.Cfg, a.Log)},
	}
	note := ""
	if !withPrompts {
		note = "Claude prompts and replies were left out (--no-prompts)."
	}
	readme := fmt.Sprintf(bundleReadme, now.Format(time.RFC3339), from.Format(time.RFC3339), now.Format(time.RFC3339), note)
	contents := map[string][]byte{"README.md": []byte(readme), "entries.jsonl": entries.Bytes()}
	order := []string{"README.md", "entries.jsonl"}
	for _, f := range files {
		b, err := json.MarshalIndent(f.data, "", "  ")
		if err != nil {
			return nil, err
		}
		contents[f.name] = append(b, '\n')
		order = append(order, f.name)
	}
	// Everything passes the redactor once more, and nothing that looks like a
	// configured secret may be left: then the file is written.
	red := a.Log.Redactor()
	for name, b := range contents {
		clean := red.String(string(b))
		for _, v := range red.Values() {
			if strings.Contains(clean, v) {
				return nil, apperr.New(apperr.Internal, "a secret was found in %s after redaction; the bug report was not written", name)
			}
		}
		contents[name] = []byte(clean)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't create the bug report file (it never replaces an existing file).").
			WithHint("Choose another name or folder.").WithDetails(map[string]any{"file": path})
	}
	zw := zip.NewWriter(fh)
	for _, name := range order {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "metarush-bug-report/" + name, Method: zip.Deflate, Modified: now})
		if err != nil {
			fh.Close()
			return nil, err
		}
		if _, err := w.Write(contents[name]); err != nil {
			fh.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		fh.Close()
		return nil, err
	}
	if err := fh.Close(); err != nil {
		return nil, err
	}
	res.Files = order
	return res, nil
}

func bundleVersions(ctx context.Context, a *app.App) map[string]any {
	tv := map[string]string{}
	for _, t := range []string{tools.FFmpeg, tools.FFprobe, tools.Node, tools.Whisper, tools.Sherpa} {
		if v := a.Tools.Version(ctx, t); v != "" {
			tv[t] = v
		}
	}
	if a.Tools.Has(tools.Claude) {
		if r, err := a.Tools.Run(tools.Quiet(ctx), tools.Spec{Tool: tools.Claude, Args: []string{"--version"}, CleanEnv: true, Env: secretpath.WithoutSecrets(os.Environ())}); err == nil {
			tv[tools.Claude] = strings.TrimSpace(strings.SplitN(string(r.Stdout), "\n", 2)[0])
		}
	}
	out := map[string]any{"metarush": version.Version, "commit": version.Commit, "built": version.Date, "go": runtime.Version(),
		"os": runtime.GOOS, "arch": runtime.GOARCH, "tools": tv, "schema": map[string]any{"code": store.SchemaVersion()}}
	if a.DB != nil {
		if v, err := a.DB.AppliedVersion(ctx); err == nil {
			out["schema"] = map[string]any{"code": store.SchemaVersion(), "database": v}
		}
		if v, rs, err := a.DB.ServerInfo(ctx); err == nil {
			out["mongodb"] = map[string]any{"version": v, "replicaSet": rs}
		}
	}
	return out
}

// configSummary describes an installation's settings without any secret:
// hosts and names only, never credentials or keys.
func configSummary(c *config.Config, l *logbook.Logger) map[string]any {
	s := l.Settings()
	media := map[string]any{"provider": c.Media.Provider}
	switch c.Media.Provider {
	case "s3":
		media["endpointHost"] = config.HostOf(c.Media.S3.Endpoint)
		media["bucket"], media["prefix"], media["preset"] = c.Media.S3.Bucket, c.Media.S3.Prefix, c.Media.S3.Preset
		media["keysConfigured"] = c.Media.S3.AccessKeyID != "" && c.Media.S3.SecretAccessKey != ""
	case "gcs":
		media["bucket"], media["prefix"], media["credentialsFile"] = c.Media.GCS.Bucket, c.Media.GCS.Prefix, c.Media.GCS.CredentialsFile != ""
	default:
		media["shared"] = c.Media.Filesystem.Shared
	}
	return map[string]any{
		"workspace":     c.Workspace,
		"installation":  c.Installation,
		"database":      map[string]any{"host": config.HostOf(c.Database.URI), "name": c.Database.Name, "local": config.IsLocalURI(c.Database.URI)},
		"media":         media,
		"cache":         map[string]any{"maxGB": c.Cache.MaxGB, "pruneIntervalMinutes": c.Cache.PruneIntervalMinutes},
		"worker":        map[string]any{"enabled": c.Worker.Enabled, "concurrency": c.Worker.Concurrency, "heavyRenders": c.Worker.HeavyRenders, "heavyBatteryThreshold": c.HeavyBatteryThreshold(), "watchEnabled": c.Worker.Watch.Enabled},
		"transcription": c.Transcription,
		"assistant":     map[string]any{"disabled": c.Assistant.Disabled, "model": c.Assistant.Model, "budgetUsd": c.Assistant.Budget(), "timeoutMinutes": c.Assistant.Timeout()},
		"dashboard":     map[string]any{"port": c.Dashboard.Port},
		"log":           map[string]any{"prompts": s.PromptsMode(), "retentionDays": s.RetentionDays},
	}
}
