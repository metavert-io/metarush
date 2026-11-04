package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catchup"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/internal/version"
)

const macAppFixtures = "../../desktop/mac/Tests/MetaRushCoreTests/Fixtures"

// The answers MetaRush.app reads (D470), written from the Go types the
// commands print, as `--json` prints them: the app's tests decode these, so a
// renamed or retyped field fails here first, not in a person's setup.
// METARUSH_UPDATE_APP_FIXTURES=1 rewrites them.
func TestMacAppFixtures(t *testing.T) {
	ok := func(v any) any { return map[string]any{"ok": true, "data": v} }
	fail := func(e *apperr.Error) any { return map[string]any{"ok": false, "error": e} }
	started := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	home := "/Users/you/.metarush"
	log := home + "/logs/daemon.log"
	app := "/Applications/MetaRush.app/Contents/Helpers/metarush"
	rendererLicence := "Remotion, which renders your videos, is free for individuals and for companies of up to 3 people; larger companies need a Remotion Company License (remotion.dev/license) before using it."

	row := func(name, title, unlocks, size string, mb, minutes int, suggested, ready bool, licence string) SetupComponentQuestion {
		st := components.ComponentState{Name: name, Title: title, Unlocks: unlocks, Features: []string{unlocks}, Suggested: suggested, Ready: ready,
			Status: map[bool]string{true: "ready", false: "missing"}[ready], Says: title + " " + map[bool]string{true: "is on this computer.", false: "isn't on this computer yet."}[ready],
			Size: size, Time: "about a minute", Licence: licence, How: "homebrew", CanRemove: false}
		return SetupComponentQuestion{ComponentState: st, Ticked: suggested && !ready, DownloadMB: mb, Minutes: minutes}
	}
	questions := SetupQuestions{Home: home,
		Storage:  SetupStorageQuestion{Suggested: "folder", Folder: home + "/media", Says: "Footage, preview copies and finished videos are kept in a folder on this computer; a cloud storage bucket your team shares is the advanced choice."},
		Database: SetupDatabaseQuestion{Suggested: "here", Here: true, DownloadMB: 85, Says: "Your projects are kept in a database on this computer: MetaRush downloads it from MongoDB (about 85 MB, free) and looks after it."},
		Port:     4845,
		Service:  SetupServiceQuestion{Suggested: true, Says: "MetaRush starts when you log in and keeps running, so the dashboard is always there."},
		Skills:   SetupSkillsQuestion{Suggested: true, ClaudeInstalled: true, Says: "Claude Code learns MetaRush's video skills, so you can edit by asking."},
		Components: []SetupComponentQuestion{
			row("ffmpeg", "FFmpeg", "Bringing in footage", "45 MB", 45, 1, true, true, ""),
			row("node", "Node.js", "Runs the renderer", "50 MB", 50, 1, true, false, ""),
			row("renderer", "The Remotion renderer", "Finished videos and review copies", "400 MB", 400, 5, true, false, rendererLicence),
			row("studio-voice", "Studio voices", "Studio voices", "1.8 GB", 1800, 15, false, false, ""),
		}}
	setUp := questions
	setUp.SetUp = true
	setUp.Port = 4851
	setUp.Database = SetupDatabaseQuestion{Suggested: "here", Here: true, Says: "Your projects are kept in a database on this computer; MetaRush keeps it running."}
	setUp.Service = SetupServiceQuestion{Suggested: false, Installed: true, Running: true, OtherCopy: true, OtherVersion: "0.1.0",
		Says: "MetaRush already starts when you log in, from another copy of MetaRush (version 0.1.0); setup leaves it as it is unless you choose to have it start this copy instead."}
	otherNewer := setUp
	otherNewer.Service = SetupServiceQuestion{Suggested: false, Installed: true, Running: true, OtherCopy: true, OtherVersion: "0.3.0", Newer: true,
		Says: "MetaRush already starts when you log in, from another copy that is newer than this one (version 0.3.0), so setup leaves it as it is."}
	otherWorker := setUp
	otherWorker.Service.WorkerOnly = true

	result := SetupResult{Home: home, Fresh: true, Workspace: config.Ref{ID: "ws_1", Name: "My Studio"}, Installation: config.Ref{ID: "ins_1", Name: "Studio Mac"},
		User:      config.UserRef{ID: "usr_1", Name: "Sam"},
		Database:  SetupDatabaseOut{Local: true, Version: "8.0.34", Port: 27150, Running: true, Name: "metarush", Says: "Your projects are kept in a database on this computer; MetaRush keeps it running."},
		Storage:   SetupStorageOut{Kind: "folder", Folder: home + "/media", Says: "Footage and finished videos are kept in a folder on this computer."},
		Dashboard: "http://127.0.0.1:4845",
		Service:   SetupServiceOut{Wanted: true, Installed: true, Running: true, Says: "MetaRush starts when you log in, and runs now."},
		Skills:    SetupSkillsOut{Wanted: true, Installed: true, Dir: "/Users/you/.claude/skills", Files: 40, Says: "Claude Code knows MetaRush's video skills."},
		Components: SetupComponentsOut{Chosen: []string{"node", "renderer"}, Added: []components.ComponentAdded{{Name: "renderer", Title: "The Remotion renderer", Done: true,
			How: "metarush", Unlocks: "Finished videos", Licence: rendererLicence, Says: "The Remotion renderer is added."}}, Ready: []string{"ffmpeg"}, LeftOut: []string{"studio-voice"},
			NeedsYou: []SetupComponentNote{{Name: "node", Title: "Node.js", Says: "Node.js needs you: install it from nodejs.org.", Command: "metarush components add node"}},
			Says:     "Added 1 optional component."},
		WhatWorks: firstrun.WhatWorks{Summary: "Most of MetaRush works free on this computer with no keys; what isn't ready yet says what it needs.",
			NoKeys: []firstrun.WorksItem{{Key: "transcripts", Name: "Transcripts and captions", Ready: true, Says: "Made on this computer."},
				{Key: "renders", Name: "Finished videos", Ready: false, Says: "Rendering needs the Remotion renderer.", Next: "Add it in Settings → Components.", Command: "metarush components add renderer", Component: "renderer"}},
			WithKeys: []firstrun.WorksItem{{Key: "ai-voices", Name: "AI voices", Says: "With an ElevenLabs key."}},
			Claude:   firstrun.ClaudeCode{Installed: true, Ready: true, Says: "Claude Code is installed and signed in."}},
		Next: []string{"metarush components add node", "metarush dashboard"}}

	status := func(installed, running, same bool, d *service.DaemonState) service.Status {
		st := service.Status{Platform: service.Launchd, Label: "metarush.daemon", File: "/Users/you/Library/LaunchAgents/metarush.daemon.plist", Installed: installed,
			Loaded: installed, Running: running, Home: home, Log: log, Daemon: d}
		if installed {
			st.Binary, st.SameBinary = app, same
			if !same {
				st.Binary, st.OtherCopy = "/opt/homebrew/opt/metarush/bin/metarush", true
			}
		}
		return st
	}
	yes, no := true, false
	// A daemon of this app's copy (its program recorded), or of another copy.
	daemon := func(v, commit string, answering bool, mode string) *service.DaemonState {
		return &service.DaemonState{PID: 4242, Port: 4845, Mode: mode, Service: "launchd", StartedAt: started, Version: v, Commit: commit, Answering: answering,
			Program: app, SameCopy: &yes}
	}
	otherDaemon := func(v, commit, svc string) *service.DaemonState {
		d := &service.DaemonState{PID: 4343, Port: 4845, Mode: "full", Service: svc, StartedAt: started, Version: v, Commit: commit, Answering: true,
			Program: "/opt/homebrew/opt/metarush/bin/metarush", SameCopy: &no}
		if c, ok := version.Compare(v, "0.2.0"); ok && c > 0 {
			d.Newer = true
		}
		return d
	}
	// The service runs ~/.local/bin/metarush, a link that leads to this app's
	// copy today: another copy to the app all the same (D473), while the
	// daemon it started says it is this copy.
	viaLink := status(true, true, true, daemon("0.1.0", "aaaaaaa", true, "full"))
	viaLink.Binary, viaLink.OtherCopy = "/Users/you/.local/bin/metarush", true
	otherWorkerStatus := status(true, true, false, &service.DaemonState{PID: 4346, Mode: "worker-only", Service: "launchd", StartedAt: started, Version: "0.1.0",
		Program: "/opt/homebrew/opt/metarush/bin/metarush", SameCopy: &no})
	otherWorkerStatus.WorkerOnly = true
	rendererReady := renderer.Status{Source: "bundled", State: "ready", Ready: true, ID: "0.2.0-1a2b3c4d", Version: "0.2.0", Remotion: "4.0.380",
		Dir: home + "/renderer/0.2.0-1a2b3c4d", VideoDir: home + "/renderer/0.2.0-1a2b3c4d/video", Browser: true, Versions: []renderer.Installed{}}
	rendererLeftOut := renderer.Status{Source: "bundled", State: "left-out", ID: "0.2.0-1a2b3c4d", Version: "0.2.0", Dir: home + "/renderer/0.2.0-1a2b3c4d",
		VideoDir: home + "/renderer/0.2.0-1a2b3c4d/video", Versions: []renderer.Installed{}}
	notSetUp := apperr.New(apperr.NotConfigured, "MetaRush isn't set up on this machine yet").WithHint("MetaRush's checkup (doctor) says what is wrong and how to fix it.").WithCommand("metarush doctor")

	files := map[string]any{
		"version-1.json":                        ok(versionOut{Info: version.Describe("0.1.0", "aaaaaaa", "2026-11-01T00:00:00Z"), EditSpec: 12}),
		"version-2.json":                        ok(versionOut{Info: version.Describe("0.2.0", "bbbbbbb", "2026-11-03T00:00:00Z"), EditSpec: 12}),
		"setup-questions.json":                  ok(questions),
		"setup-questions-set-up.json":           ok(setUp),
		"setup-questions-other-newer.json":      ok(otherNewer),
		"setup-questions-other-worker.json":     ok(otherWorker),
		"setup.json":                            ok(result),
		"not-set-up.json":                       fail(notSetUp),
		"setup-failed.json":                     fail(apperr.New(apperr.Database, "The database on this computer couldn't be set up").WithHint("Run setup again: it carries on where it stopped.").WithCommand("metarush setup")),
		"service-status-none.json":              ok(status(false, false, false, nil)),
		"service-status-old.json":               ok(status(true, true, true, daemon("0.1.0", "aaaaaaa", true, "full"))),
		"service-status-new.json":               ok(status(true, true, true, daemon("0.2.0", "bbbbbbb", true, "full"))),
		"service-status-stopped.json":           ok(status(true, false, true, nil)),
		"service-status-other-copy.json":        ok(status(true, true, false, otherDaemon("0.1.0", "aaaaaaa", "launchd"))),
		"service-status-other-newer.json":       ok(status(true, true, false, otherDaemon("0.3.0", "ccccccc", "launchd"))),
		"service-status-other-worker.json":      ok(otherWorkerStatus),
		"service-status-via-link.json":          ok(viaLink),
		"service-status-dashboard-old.json":     ok(status(false, false, false, &service.DaemonState{PID: 4343, Port: 4845, Mode: "full", StartedAt: started, Version: "0.1.0", Commit: "aaaaaaa", Answering: true, Program: app, SameCopy: &yes})),
		"service-status-dashboard-new.json":     ok(status(false, false, false, &service.DaemonState{PID: 4344, Port: 4845, Mode: "full", StartedAt: started, Version: "0.2.0", Commit: "bbbbbbb", Answering: true, Program: app, SameCopy: &yes})),
		"service-status-dashboard-other.json":   ok(status(false, false, false, otherDaemon("0.1.0", "aaaaaaa", ""))),
		"service-status-dashboard-unknown.json": ok(status(false, false, false, &service.DaemonState{PID: 4347, Port: 4845, Mode: "full", StartedAt: started, Version: "0.1.0", Commit: "aaaaaaa", Answering: true})),
		"service-status-worker-new.json":        ok(status(true, true, true, &service.DaemonState{PID: 4345, Mode: "worker-only", Service: "launchd", StartedAt: started, Version: "0.2.0", Program: app, SameCopy: &yes})),
		"service-restart.json":                  ok(ServiceRestartOut{Status: status(true, true, true, nil), Started: true}),
		"service-install.json":                  ok(ServiceInstallOut{Plan: service.Plan{File: "/Users/you/Library/LaunchAgents/metarush.daemon.plist"}, Status: status(true, true, true, nil), Started: true, Waited: true}),
		"service-install-newer.json": fail(apperr.New(apperr.Conflict, "MetaRush runs a newer copy (version 0.3.0) than this one, so this one doesn't take its place.").
			WithHint("Update this copy first, or keep the newer one running.").WithDetails(map[string]any{"running": "0.3.0", "this": "0.2.0"})),
		"service-restart-failed.json": fail(apperr.New(apperr.ToolFailed, "MetaRush did not start again.").WithHint("Its log says why.").WithCommand("metarush service logs")),
		"skills-dry-none.json":        ok(skillsinstall.Result{Dir: "/Users/you/.claude/skills", Installed: []string{"video-render/SKILL.md"}, Updated: []string{}, Unchanged: []string{}, Skipped: []string{}}),
		"skills-dry-there.json":       ok(skillsinstall.Result{Dir: "/Users/you/.claude/skills", Installed: []string{}, Updated: []string{"video-render/SKILL.md"}, Unchanged: []string{"video-qa/SKILL.md"}, Skipped: []string{}}),
		"skills-install.json":         ok(skillsinstall.Result{Dir: "/Users/you/.claude/skills", Installed: []string{}, Updated: []string{"video-render/SKILL.md"}, Unchanged: []string{"video-qa/SKILL.md"}, Skipped: []string{}}),
		"renderer-install.json":       ok(rendererReady),
		"renderer-left-out.json":      ok(RendererNotNow{Status: rendererLeftOut, Says: rendererLeftOutNote, Command: "metarush renderer install --force"}),
		"migrate.json":                ok(MigrateOut{Applied: []string{"63 review desk"}, CatchUp: []catchup.Outcome{}}),
		"migrate-failed.json":         fail(apperr.New(apperr.Database, "MetaRush can't reach its database.").WithHint("Its checkup says why.").WithCommand("metarush doctor")),
		"stop.json":                   ok(StopOut{Stopped: true, PID: 4343}),
		"dashboard.json":              ok(DashboardOut{URL: "http://127.0.0.1:4845/auth?t=nonce", ExpiresInMinutes: 10, Dashboard: "http://127.0.0.1:4845"}),
		"doctor.json": ok(map[string]any{"ok": false, "version": "0.2.0", "checks": []Check{
			{Area: "system", Name: "metarush", Status: "info", Detail: "0.2.0 darwin/arm64", Say: "MetaRush 0.2.0"},
			{Area: "database", Name: "migrations", Status: "warn", Detail: "1 update(s) waiting", Fix: "run `metarush migrate`", Say: "The database needs 1 update for this version of MetaRush", Do: "Update the database."},
			{Area: "storage", Name: "media", Status: "fail", Detail: "/Volumes/Footage not found", Say: "The storage folder isn't there", Do: "Connect the drive that holds it."},
			{Area: "tools", Name: "ffmpeg", Status: "ok", Detail: "9.0"}}}),
	}
	if os.Getenv("METARUSH_UPDATE_APP_FIXTURES") == "1" {
		if err := os.MkdirAll(macAppFixtures, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ") // as emit and failErr print it for people
		if err := enc.Encode(files[n]); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(macAppFixtures, n)
		if os.Getenv("METARUSH_UPDATE_APP_FIXTURES") == "1" {
			if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(have, b.Bytes()) {
			t.Errorf("%s is out of date (an answer MetaRush.app reads changed): run\n  METARUSH_UPDATE_APP_FIXTURES=1 go test ./internal/cli -run TestMacAppFixtures\nthen the app's tests (scripts/mac-app.sh test)", path)
		}
	}
}
