package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/machines"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/service"
)

// setupPlain fails when plain output holds what a person should not have to
// read (plainwords: IDs, paths, commands, flags other than --details, codes, jargon).
func setupPlain(t *testing.T, what, out string) {
	t.Helper()
	allow := plainwords.Allow{"flag:--details"}
	for _, f := range allow.Filter(plainwords.CheckShown(out)) {
		t.Errorf("plain %s shows %s:\n%s", what, f, out)
	}
}

// setupCmd is a command whose --details is on or off (detailsMode, readyToSave).
func setupCmd(details bool) *cobra.Command {
	c := &cobra.Command{Use: "metarush"}
	c.Flags().Bool("details", details, "")
	return c
}

// doctor speaks plainly: one line per check that has something to say, the
// next step in words, areas a person knows; --details adds each check's
// technical line and fix, as --json has them.
func TestDoctorChecksArePlain(t *testing.T) {
	checks := []Check{
		{Area: "system", Name: "metarush", Status: "info", Detail: "0.9.0 (abc123, 2026-10-01) darwin/arm64", Say: "MetaRush 0.9.0"},
		{Area: "security", Name: "home location", Status: "ok", Detail: "outside git repositories and synced folders"},
		{Area: "config", Name: "home", Status: "ok", Detail: "/Users/you/.metarush"},
		{Area: "security", Name: ".env location", Status: "ok", Detail: "/Users/you/.metarush/.env (outside repositories and synced folders)"},
		{Area: "database", Name: "connection", Status: "ok", Detail: "127.0.0.1:27018 / metarush (MongoDB 8.0.34)", Say: "Database: connected, and live updates work"},
		{Area: "database", Name: "replica set", Status: "ok", Detail: "change streams and transactions available"},
		{Area: "database", Name: "migrations", Status: "warn", Detail: "schema v18, 1 update(s) waiting (latest v19)", Fix: "run `metarush migrate`",
			Say: "The database needs 1 update for this version of MetaRush", Do: "Update the database."},
		{Area: "workspace", Name: "workspace", Status: "ok", Detail: "Studio (ws_01m5bfrkcjwn19cxqm0ggtpsmq)", Say: "Workspace “Studio” (2 machines)"},
		{Area: "worker", Name: "job types", Status: "info", Detail: "contact_sheet, describe, transcribe"},
		{Area: "dashboard", Name: "daemon", Status: "info", Detail: "not running", Fix: "start with `metarush dashboard`", Say: "The dashboard isn't running", Do: "Open the dashboard to start it."},
	}
	plain := stdout(t, func() { printChecks(checks, false); fmt.Println(checksFooter(checks)) })
	setupPlain(t, "doctor", plain)
	for _, want := range []string{"THIS MACHINE", "SAFETY", "Passwords and keys are kept safe", "DATABASE", "Database: connected", "→ Update the database.",
		"WORKSPACE", "Workspace “Studio” (2 machines)", "→ Open the dashboard to start it.", "(Add --details to see the commands that fix these.)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain doctor lacks %q:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "SAFETY") != 1 || strings.Contains(plain, "BACKGROUND WORK") {
		t.Errorf("areas are printed once, and an area with nothing to say is left out:\n%s", plain)
	}
	detailed := stdout(t, func() { printChecks(checks, true) })
	for _, want := range []string{"connection: 127.0.0.1:27018 / metarush", "fix: run `metarush migrate`", "home: /Users/you/.metarush", "ws_01m5bfrkcjwn19cxqm0ggtpsmq",
		"job types: contact_sheet", "fix: start with `metarush dashboard`"} {
		if !strings.Contains(detailed, want) {
			t.Errorf("--details doctor lacks %q:\n%s", want, detailed)
		}
	}
	// A problem never stays hidden, even without plain words of its own.
	odd := stdout(t, func() {
		printChecks([]Check{{Area: "storage", Name: "x", Status: "fail", Detail: "something broke"}}, false)
	})
	if !strings.Contains(odd, "✗ Something broke") {
		t.Errorf("a failing check without plain words:\n%s", odd)
	}
}

// A checked narration or mix change ends with readyToSave: plain says nothing
// changed yet; --details gives the command with its confirm token.
func TestCheckedChangeIsReadyToSave(t *testing.T) {
	check := &ops.EditCheckOut{Valid: true, Diff: &editspec.Diff{Summary: []string{"2 narration lines changed"}}, ConfirmToken: "ct_1792489378_b7d3c7939258076a8efc9c8aa15be2ee", // gitleaks:allow (made up for the test)
		Warnings: []editspec.Issue{{Code: "music_rights_missing", Path: "/audio/0", Message: "The music bed has no rights recorded."}}}
	file := "/Users/you/.metarush/scratch/plans/plan-8ca2ace68433.json"
	plain := stdout(t, func() { printCheckPlain(setupCmd(false), check, file) })
	setupPlain(t, "checked change", plain)
	for _, want := range []string{"What would change:", "2 narration lines changed", "The music bed has no rights recorded.", "Ready to save. Nothing has changed yet."} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain checked change lacks %q:\n%s", want, plain)
		}
	}
	detailed := stdout(t, func() { printCheckPlain(setupCmd(true), check, file) })
	if !strings.Contains(detailed, "next: metarush edit apply --plan "+file+" --confirm "+check.ConfirmToken) || !strings.Contains(detailed, "music_rights_missing") {
		t.Errorf("--details checked change:\n%s", detailed)
	}
	bad := stdout(t, func() {
		printCheckPlain(setupCmd(false), &ops.EditCheckOut{Errors: []editspec.Issue{{Code: "beat_unresolved", Message: "Line 2 starts at a shot that isn't in the cut."}}}, file)
	})
	if !strings.Contains(bad, "can't be made yet") || !strings.Contains(bad, "Nothing has changed.") || strings.Contains(bad, "beat_unresolved") {
		t.Errorf("invalid change:\n%s", bad)
	}
}

// Setup and status screens name machines and say what is going on; keys,
// folders and IDs are behind --details.
func TestSetupStatusOutputIsPlain(t *testing.T) {
	l := &machines.MachineList{Workspace: config.Ref{ID: "ws_01m5bfrkcjwn19cxqm0ggtpsmq", Name: "Studio"}, KeyProvider: "tigris",
		Machines: []machines.MachineRow{{ID: "ins_01m5bfrkcjwn19cxqm0ggtpsmq", Name: "studio-mac", State: "active", Current: true, KeyLabel: "metarush-studio-mac",
			KeyID: "tid_••••a0b1", KeyStatus: "unchecked", DBUser: "metarush-studio-mac"}, {Name: "mini", State: "invited", KeyStatus: "none"}}}
	plain := stdout(t, func() { printMachines(l, false) })
	setupPlain(t, "machine list", plain)
	if !strings.Contains(plain, "studio-mac (this machine)") || !strings.Contains(plain, "invited, not joined yet") || strings.Contains(plain, "metarush-studio-mac") {
		t.Errorf("plain machine list:\n%s", plain)
	}
	if d := stdout(t, func() { printMachines(l, true) }); !strings.Contains(d, "metarush-studio-mac") || !strings.Contains(d, "tid_••••a0b1") || !strings.Contains(d, "ws_01m5bfrkcjwn19cxqm0ggtpsmq") {
		t.Errorf("--details machine list:\n%s", d)
	}

	svc := service.Status{Installed: true, Loaded: true, Running: true, Platform: "launchd", Label: "io.metarush.agent", File: "/Users/you/Library/LaunchAgents/io.metarush.agent.plist",
		Binary: "/usr/local/bin/metarush", Log: "/Users/you/.metarush/logs/daemon.log", PID: 4242}
	plain = stdout(t, func() { printServiceStatus(svc, false) })
	setupPlain(t, "service status", plain)
	if !strings.Contains(plain, "MetaRush is running in the background with the dashboard") {
		t.Errorf("plain service status:\n%s", plain)
	}
	if d := stdout(t, func() { printServiceStatus(svc, true) }); !strings.Contains(d, "io.metarush.agent.plist") || !strings.Contains(d, "pid 4242") {
		t.Errorf("--details service status:\n%s", d)
	}
	none := stdout(t, func() { printServiceStatus(service.Status{}, false) })
	setupPlain(t, "service status (none)", none)

	st := renderer.Status{Source: renderer.SourceBundled, State: renderer.StateDamaged, ID: "0.9.0-ab12cd", Version: "0.9.0", Remotion: "4.0.536",
		Dir: "/Users/you/.metarush/renderer/0.9.0-ab12cd", Problem: "The renderer for this version is damaged (its setup record is missing).",
		Fix: "Run `metarush renderer install` to set it up again."}
	plain = stdout(t, func() { printRendererStatus(st, false) })
	setupPlain(t, "renderer status", plain)
	if !strings.Contains(plain, "The renderer for this version is damaged.") || !strings.Contains(plain, "Set it up again.") {
		t.Errorf("plain renderer status:\n%s", plain)
	}
	if d := stdout(t, func() { printRendererStatus(st, true) }); !strings.Contains(d, "fix: Run `metarush renderer install`") || !strings.Contains(d, "Remotion 4.0.536") {
		t.Errorf("--details renderer status:\n%s", d)
	}

	cs := cache.Stats{Dir: "/Users/you/.metarush/cache", Objects: 3, Bytes: 3 << 20, MaxBytes: 50 << 30, Pinned: 1, PinnedBytes: 1 << 20}
	plain = stdout(t, func() { printCacheStats(cs, false) })
	setupPlain(t, "cache status", plain)
	if !strings.Contains(plain, "This machine's copy uses") || !strings.Contains(plain, "kept here for offline work") {
		t.Errorf("plain cache status:\n%s", plain)
	}
	if d := stdout(t, func() { printCacheStats(cs, true) }); !strings.Contains(d, "/Users/you/.metarush/cache") {
		t.Errorf("--details cache status:\n%s", d)
	}

	sy := &ops.SyncStatusOut{Summary: []string{"This machine has copies of 1 of the 2 files this workspace uses."},
		Assets: []ops.SyncAsset{{ID: "ast_01m5bfrkcjwn19cxqm0ggtpsmq", Name: "Garage interview", State: "available", CanonicalOnlyBytes: 2 << 20,
			Objects: []ops.SyncObject{{Role: "proxy", Size: 2 << 20}, {Role: "render_thumbnail", Size: 1 << 10, Cached: true}}}},
		Jobs: ops.SyncJobs{Jobs: []ops.SyncJob{{ID: "job_01m5bfrkcjwn19cxqm0ggtpsmq", Type: "transcribe", State: "queued"}}}}
	plain = stdout(t, func() { printSync(sy, false, false) })
	setupPlain(t, "sync status", plain)
	if !strings.Contains(plain, "Garage interview") || !strings.Contains(plain, "preview copy") || !strings.Contains(plain, "waiting") {
		t.Errorf("plain sync status:\n%s", plain)
	}
	if d := stdout(t, func() { printSync(sy, false, true) }); !strings.Contains(d, "ast_01m5bfrkcjwn19cxqm0ggtpsmq") || !strings.Contains(d, "job_01m5bfrkcjwn19cxqm0ggtpsmq") {
		t.Errorf("--details sync status:\n%s", d)
	}

	res := &BundleOut{File: "/Users/you/Downloads/metarush-bug-report-20261019-101500.zip", Since: time.Now().Add(-2 * time.Hour), Entries: 1, FailedJobs: 2}
	plain = stdout(t, func() { printBundle(res, false) })
	if !strings.Contains(plain, "1 log entry and 2 failed jobs") || strings.Contains(plain, "/Users/you/Downloads/") {
		t.Errorf("plain bug report:\n%s", plain)
	}
	if d := stdout(t, func() { printBundle(res, true) }); !strings.Contains(d, "file: "+res.File) {
		t.Errorf("--details bug report:\n%s", d)
	}
}
