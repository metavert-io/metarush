package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/worker"
)

// Check is one diagnostic. Area, Name, Status, Detail and Fix are the
// technical record (doctor --json, --details, bug reports). Say and Do are the
// same check for people (skills/_shared/voice.md): what it means and what to
// do next, without paths, versions or commands. A check that is fine and has
// nothing to tell a person has no Say: it shows only with --details.
type Check struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | info
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	Say    string `json:"-"`
	Do     string `json:"-"`
}

// addCheck records one check: its technical detail and fix, then, in plain
// words, what it means (plain[0]) and what to do (plain[1]).
type addCheck func(area, name, status, detail, fix string, plain ...string)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that MetaRush is ready on this machine: video tools, database, storage and safety",
		RunE: func(c *cobra.Command, _ []string) error {
			checks := runDoctor(c.Context())
			ok := true
			for _, ch := range checks {
				if ch.Status == "fail" {
					ok = false
				}
			}
			err := emit(c, map[string]any{"ok": ok, "version": version.Version, "checks": checks}, func() {
				printChecks(checks, detailsMode(c))
				if ok {
					fmt.Println("\nNo blocking problems found.")
				} else {
					fmt.Println("\nSome things need fixing: see the steps above.")
				}
				if !detailsMode(c) {
					fmt.Println(checksFooter(checks))
				}
			})
			if err == nil && !ok {
				return &exitError{code: 1}
			}
			return err
		},
	}
}

// checkTitles names doctor's areas the way people know them (several areas
// can share a title: they are printed together).
var checkTitles = map[string]string{
	"system": "THIS MACHINE", "config": "THIS MACHINE", "security": "SAFETY", "tools": "VIDEO TOOLS",
	"link imports": "IMPORTING FROM LINKS", "on-screen index": "LOOKING AT YOUR FOOTAGE", "renderer": "RENDERS",
	"database": "DATABASE", "workspace": "WORKSPACE", "storage": "STORAGE", "worker": "BACKGROUND WORK", "dashboard": "DASHBOARD",
	"clearer voices": "CLEARER VOICES", "google drive": "GOOGLE DRIVE", "claude code": "CLAUDE CODE", "components": "COMPONENTS",
}

// allGood is what an area says when nothing in it needs a look and none of
// its checks has anything of its own to say.
var allGood = map[string]string{
	"SAFETY": "Passwords and keys are kept safe, and the dashboard only opens on this computer",
}

var checkIcon = map[string]string{"ok": "✓", "warn": "!", "fail": "✗", "info": "·"}

// printChecks prints doctor's checks for people: grouped by what they are
// about, one plain line each and the next step in words. --details adds each
// check's technical line (its name and detail, as --json has them) and fix.
func printChecks(checks []Check, details bool) {
	type group struct {
		title  string
		checks []Check
	}
	var groups []*group
	byTitle := map[string]*group{}
	for _, ch := range checks {
		t := checkTitles[ch.Area]
		if t == "" {
			t = strings.ToUpper(ch.Area)
		}
		g := byTitle[t]
		if g == nil {
			g = &group{title: t}
			byTitle[t] = g
			groups = append(groups, g)
		}
		g.checks = append(g.checks, ch)
	}
	for _, g := range groups {
		var b strings.Builder
		good, said := true, false
		for _, ch := range g.checks {
			if ch.Status == "warn" || ch.Status == "fail" {
				good = false
			}
			if plainSay(ch) != "" {
				said = true
			}
		}
		if s := allGood[g.title]; s != "" && good && !said {
			fmt.Fprintf(&b, "  ✓ %s\n", s)
		}
		for _, ch := range g.checks {
			say := plainSay(ch)
			problem := ch.Status != "ok"
			switch {
			case say != "":
				fmt.Fprintf(&b, "  %s %s\n", checkIcon[ch.Status], say)
				if ch.Do != "" && problem {
					fmt.Fprintf(&b, "    → %s\n", ch.Do)
				}
				if details {
					fmt.Fprintf(&b, "      %s: %s\n", ch.Name, ch.Detail)
				}
			case details:
				fmt.Fprintf(&b, "  %s %s: %s\n", checkIcon[ch.Status], ch.Name, ch.Detail)
			}
			if details && ch.Fix != "" && problem {
				fmt.Fprintf(&b, "      fix: %s\n", ch.Fix)
			}
		}
		if b.Len() > 0 {
			fmt.Printf("\n%s\n%s", g.title, b.String())
		}
	}
}

// plainSay is a check's plain line. A problem always gets one: one without
// plain words of its own falls back to its detail rather than staying hidden.
func plainSay(ch Check) string {
	if ch.Say == "" && (ch.Status == "warn" || ch.Status == "fail") {
		return strings.TrimSuffix(sentence(ch.Detail), ".")
	}
	return ch.Say
}

// checksFooter points to --details: the commands that fix what needs fixing,
// or the versions and folders behind the plain lines.
func checksFooter(checks []Check) string {
	for _, ch := range checks {
		if ch.Status != "ok" && ch.Fix != "" {
			return "(Add --details to see the commands that fix these.)"
		}
	}
	return "(Add --details to see versions, folders and other technical detail.)"
}

func runDoctor(parent context.Context) []Check {
	var out []Check
	add := func(area, name, status, detail, fix string, plain ...string) {
		ch := Check{Area: area, Name: name, Status: status, Detail: detail, Fix: fix}
		if len(plain) > 0 {
			ch.Say = plain[0]
		}
		if len(plain) > 1 {
			ch.Do = plain[1]
		}
		out = append(out, ch)
	}
	add("system", "metarush", "info", fmt.Sprintf("%s %s/%s", version.String(), runtime.GOOS, runtime.GOARCH), "", version.Current().Plain())
	if underRosetta() {
		add("system", "architecture", "warn", "running under Rosetta (Intel build on Apple Silicon)", "install the darwin/arm64 build for full speed",
			"MetaRush is running as an Intel app on this Apple silicon Mac, so it is slower than it could be", "Install the Apple silicon version of MetaRush for full speed.")
	}
	h, err := home()
	if err != nil {
		add("config", "home", "fail", err.Error(), "", "MetaRush can't find where it keeps its settings on this machine")
		return out
	}
	homeSecurityChecks(h, add) // before Load: Load itself refuses an unsafe <home>/.env
	cfg, err := config.Load(h)
	if err != nil {
		if config.Exists(h) {
			add("config", "config", "fail", err.Error(), "run `metarush init`", "MetaRush's settings on this machine couldn't be read", "Set MetaRush up again on this machine.")
		} else {
			add("config", "config", "fail", err.Error(), "run `metarush init`", "MetaRush isn't set up on this machine yet", "Set it up first.")
		}
		ts := tools.New(config.Tools{})
		toolChecks(parent, ts, add, config.Default(h))
		claudeChecks(parent, ts, add)
		return out
	}
	add("config", "home", "ok", h, "")
	envSecretsCheck(h, cfg, add)
	if b, err := os.ReadFile(config.Path(h)); err == nil {
		low := string(b)
		if strings.Contains(low, "mongodb://") && strings.Contains(low, "@") || strings.Contains(low, "mongodb+srv://") && strings.Contains(low, "@") {
			add("security", "secrets in config", "fail", "config.yaml contains a MongoDB URI with credentials", "rerun `metarush init --reconfigure` (stores it in the keychain)",
				"The database password is written in MetaRush's settings file", "Set MetaRush up again so the password goes into the keychain.")
		} else {
			add("security", "secrets in config", "ok", "no plaintext credentials in config.yaml", "")
		}
	}
	if cfg.Dashboard.Host != "127.0.0.1" && cfg.Dashboard.Host != "localhost" {
		add("security", "dashboard bind", "warn", "dashboard listens on "+cfg.Dashboard.Host+" (reachable from the network)", "bind to 127.0.0.1 unless you have TLS + auth in front",
			"Other computers on your network can open the dashboard", "Keep it on this computer only, unless a sign-in and encryption protect it.")
	} else {
		add("security", "dashboard bind", "ok", "127.0.0.1 only (Host-checked, one-time sign-in links)", "")
	}
	ts := tools.New(cfg.Tools)
	remoteChecks(parent, cfg, ts, add) // remote.go: remote access over Tailscale, when it is on
	toolChecks(parent, ts, add, cfg)
	componentChecks(parent, cfg, ts, add) // the optional components: how many are here (D324)
	claudeChecks(parent, ts, add)
	importChecks(parent, cfg, ts, add)
	visionChecks(parent, cfg, ts, add)
	soundChecks(cfg, ts, add)      // doctor_sound.go: clean and studio, set up or not
	driveChecks(parent, ts, add)   // doctor_drive.go: Google Drive folders through the person's own rclone (D220)
	dropboxChecks(parent, ts, add) // and Dropbox folders the same way (D306)
	rendererChecks(cfg, ts, add)   // renderer.go: this version's renderer (no database needed)
	if l := cfg.Database.Local; l != nil {
		localDatabaseChecks(parent, cfg, l, ts, add) // the database on this computer (D254), started when it isn't running
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, cfg.Database.URI, cfg.Database.Name)
	if err != nil {
		add("database", "connection", "fail", err.Error(), "check METARUSH_MONGODB_URI / keychain entry and network access",
			"MetaRush can't reach its database", "Check the internet connection and MetaRush's database settings.")
		return out
	}
	defer db.Close(context.Background())
	ver, rs, _ := db.ServerInfo(ctx)
	connected := "Database: connected"
	if rs {
		connected = "Database: connected, and live updates work"
	}
	add("database", "connection", "ok", fmt.Sprintf("%s / %s (MongoDB %s)", config.HostOf(cfg.Database.URI), cfg.Database.Name, ver), "", connected)
	if rs {
		add("database", "replica set", "ok", "change streams and transactions available", "")
	} else {
		add("database", "replica set", "warn", "standalone server: live updates use polling", "run MongoDB as a replica set (Atlas always is)",
			"Live updates are slower: the dashboard checks for changes every few seconds", "For instant updates, use MongoDB Atlas or run the database as a replica set.")
	}
	if !config.IsLocalURI(cfg.Database.URI) {
		if strings.HasPrefix(cfg.Database.URI, "mongodb+srv://") || strings.Contains(cfg.Database.URI, "tls=true") {
			add("database", "TLS", "ok", "encrypted connection", "")
		} else {
			add("database", "TLS", "fail", "remote database without TLS", "use mongodb+srv:// or tls=true",
				"The connection to the database isn't encrypted", "Turn on encryption for the database connection.")
		}
	}
	if strings.Contains(config.HostOf(cfg.Database.URI), ".mongodb.net") {
		if cfg.Database.AtlasOrgID == "" {
			add("database", "Atlas organization", "warn", "not recorded — the URI alone cannot prove which organization owns this cluster", "verify with `atlas organizations list` and run init --reconfigure --atlas-org-id …",
				"It isn't recorded which MongoDB Atlas organization owns the database", "Check who owns it, and record it the next time you set MetaRush up.")
		} else {
			add("database", "Atlas organization", "ok", fmt.Sprintf("%s (%s), project %s — recorded at setup", cfg.Database.AtlasOrgName, cfg.Database.AtlasOrgID, cfg.Database.AtlasProjectID), "")
		}
	}
	if v, err := db.AppliedVersion(ctx); err == nil {
		pending, _ := db.PendingMigrations(ctx)
		if len(pending) > 0 {
			add("database", "migrations", "warn", fmt.Sprintf("schema v%d, %d update(s) waiting (latest v%d)", v, len(pending), store.SchemaVersion()), "run `metarush migrate`",
				fmt.Sprintf("The database needs %s for this version of MetaRush", plural(len(pending), "update")), "Update the database.")
		} else {
			add("database", "migrations", "ok", fmt.Sprintf("schema v%d", v), "")
		}
	}
	var ws models.Workspace
	if err := db.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": cfg.Workspace.ID}).Decode(&ws); err != nil {
		add("workspace", "workspace", "fail", "workspace "+cfg.Workspace.ID+" not found in this database", "init again with --join",
			"This machine's workspace isn't in this database", "Set this machine up again and join the right workspace.")
		return out
	}
	n, _ := db.C(store.CInstallations).CountDocuments(ctx, bson.M{"workspaceId": ws.ID})
	add("workspace", "workspace", "ok", fmt.Sprintf("%s (%s)", ws.Name, ws.ID), "", fmt.Sprintf("Workspace “%s” (%s)", ws.Name, plural(int(n), "machine")))
	add("workspace", "installations", "info", fmt.Sprintf("%d machine(s) joined", n), "")
	var self models.Installation
	if err := db.C(store.CInstallations).FindOne(ctx, bson.M{"_id": cfg.Installation.ID, "workspaceId": ws.ID}).Decode(&self); err == nil && self.State == models.InstallationRevoked {
		add("workspace", "this installation", "fail", "revoked — its credentials were withdrawn", "ask a workspace admin for a new invite (`metarush machine invite <name>`)",
			"This machine was removed from the workspace", "Ask whoever runs the workspace to invite it again.")
	} else if err == nil && self.StorageKey != nil {
		add("workspace", "this installation", "ok", fmt.Sprintf("%s with its own storage key %s (%s scope)", self.Name, self.StorageKey.Label, self.StorageKey.Scope), "")
	}
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		add("storage", "media store", "fail", err.Error(), "", "MetaRush's storage can't be opened", "Check MetaRush's storage settings.")
		return out
	}
	defer a.Close()
	if err := storeRoundTrip(ctx, a.Media); err != nil {
		add("storage", "media store", "fail", a.Media.Locator()+": "+err.Error(), "check bucket/endpoint/keys or the media root",
			"MetaRush's storage isn't working: a test file couldn't be saved and read back", "Check the storage settings and its keys.")
	} else {
		add("storage", "media store", "ok", fmt.Sprintf("%s — write, ranged read, delete OK", a.Media.Locator()), "", "MetaRush's storage works")
	}
	if g, ok := a.Media.(*media.GCS); ok {
		add("storage", "gcs credentials", "ok", g.Auth(), "")
		if f := cfg.Media.GCS.CredentialsFile; f != "" {
			if st, err := os.Stat(f); err == nil && st.Mode().Perm()&0o077 != 0 {
				add("security", "gcs key file", "warn", fmt.Sprintf("%s is %v (readable by other users)", f, st.Mode().Perm()), "chmod 600 it",
					"Other people on this computer can read the Google Cloud key file", "Make the file readable only by you.")
			} else if err == nil {
				add("security", "gcs key file", "ok", f+" (0600; path stored in config, contents never copied)", "")
			}
		}
		if ok, note := g.SigningStatus(); ok {
			add("storage", "signed URLs", "ok", note+" — the dashboard reads media directly from GCS", "")
		} else {
			add("storage", "signed URLs", "info", note, "for direct browser reads use a key file, or grant the attached service account roles/iam.serviceAccountTokenCreator on itself")
		}
		if h := os.Getenv("STORAGE_EMULATOR_HOST"); h != "" {
			add("storage", "gcs emulator", "warn", "STORAGE_EMULATOR_HOST="+h+": GCS traffic goes to an emulator, not Google", "unset it to use the real service",
				"Storage goes to a test stand-in, not the real Google Cloud", "Remove that test setting to use the real service.")
		}
	}
	if ws.MediaStore.Locator != "" && ws.MediaStore.Locator != a.Media.Locator() {
		add("storage", "canonical store", "fail", fmt.Sprintf("workspace uses %s but this machine is configured for %s", ws.MediaStore.Locator, a.Media.Locator()), "point this installation at the workspace's store",
			"This machine uses different storage than the rest of the workspace, so you won't see the same footage", "Point this machine at the workspace's storage.")
	}
	if !a.Media.Capabilities().Shared && n > 1 {
		add("storage", "sharing", "fail", "this media store is local to one machine but the workspace has several installations", "use an S3-compatible bucket (Tigris, R2, B2, S3, GCS) or a shared mount",
			"The workspace has several machines, but its storage is on one machine's disk, so the others can't reach the footage", "Use shared storage: a cloud bucket or a shared drive.")
	}
	cs, _ := a.Cache.Status()
	add("storage", "local cache", "ok", fmt.Sprintf("%s — %d objects, %s of %s (%d pinned)", cs.Dir, cs.Objects, fmtBytes(cs.Bytes), fmtBytes(cs.MaxBytes), cs.Pinned), "",
		fmt.Sprintf("This machine's copy uses %s of its %s", fmtBytes(cs.Bytes), fmtBytes(cs.MaxBytes)))
	switch q := cs.Quota; {
	case cfg.Cache.PruneInterval() == 0:
		add("storage", "cache quota", "info", "automatic pruning is off (cache.pruneIntervalMinutes < 0)", "run `metarush cache prune` yourself",
			"This machine's copy isn't trimmed automatically", "Clear older files from it yourself now and then.")
	case q != nil && q.OverQuota:
		add("storage", "cache quota", "warn", q.Note, "unpin something (pins.txt) or raise cache.maxGB",
			"This machine's copy is over its size limit", "Unpin something, or raise the limit.")
	case q != nil && q.Enforced:
		add("storage", "cache quota", "ok", fmt.Sprintf("enforced by the daemon (pid %d) every %s", q.EnforcerPID, cfg.Cache.PruneInterval()), "")
	case cs.MaxBytes > 0 && cs.Bytes > cs.MaxBytes:
		add("storage", "cache quota", "warn", "over the limit and no daemon is enforcing it", "start `metarush dashboard` or run `metarush cache prune`",
			"This machine's copy is over its size limit, and nothing is trimming it right now", "Start MetaRush, or clear older files from it now.")
	default:
		add("storage", "cache quota", "info", "enforced while the daemon or a worker runs (every "+cfg.Cache.PruneInterval().String()+")", "")
	}
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(cfg.Scratch.Dir, &sfs); err == nil {
		free := int64(sfs.Bavail) * int64(sfs.Bsize)
		if free < 20<<30 {
			add("storage", "free disk", "warn", fmtBytes(free)+" free on the scratch volume", "renders and proxies need room; free some disk space",
				"Only "+fmtBytes(free)+" free on this disk", "Free some disk space: renders and preview copies need room.")
		} else {
			add("storage", "free disk", "ok", fmtBytes(free)+" free on the scratch volume", "", fmtBytes(free)+" free on this disk")
		}
	}
	w := worker.New(a)
	add("worker", "job types", "info", strings.Join(w.JobTypes(), ", "), "")
	online, plain, _ := activeWorkers(ctx, a)
	add("worker", "online workers", "info", online, "", plain)
	url := fmt.Sprintf("http://%s:%d/api/health", cfg.Dashboard.Host, cfg.Dashboard.Port)
	client := http.Client{Timeout: 2 * time.Second}
	if res, err := client.Get(url); err == nil {
		res.Body.Close()
		add("dashboard", "daemon", "ok", "running on port "+fmt.Sprint(cfg.Dashboard.Port), "", "The dashboard is running")
	} else {
		add("dashboard", "daemon", "info", "not running", "start with `metarush dashboard`", "The dashboard isn't running", "Open the dashboard to start it.")
	}
	return out
}

// toolChecks reports the video tools and transcription through the
// components' one detection (D324): each is optional, so a missing one is a
// warning that says what it unlocks and that Settings → Components adds it
// (metarush components add …), never ./bootstrap.sh.
func toolChecks(ctx context.Context, ts *tools.Set, add addCheck, cfg *config.Config) {
	e := componentsEnv(cfg, ts)
	missing := func(area, name string, f components.Found) {
		c, _ := components.Get(name)
		add(area, name, "warn", firstNonEmpty(f.Partly, "not on this computer"), "metarush components add "+name,
			strings.TrimSuffix(firstNonEmpty(f.Partly, c.Needed()), "."), "Add it in Settings → Components.")
	}
	allOK := true
	for _, name := range []string{"ffmpeg", "node"} {
		c, _ := components.Get(name)
		f := e.Find(ctx, c)
		if !f.Ready {
			missing("tools", name, f)
			allOK = false
			continue
		}
		for _, t := range c.Tools {
			if !ts.Has(t) {
				continue
			}
			arch := tools.Arch(ts.Path(t))
			detail := fmt.Sprintf("%s %s (%s)", ts.Version(ctx, t), ts.Path(t), arch)
			if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && arch == "x86_64" {
				add("tools", t, "warn", detail, "this Intel build runs under Rosetta; install a native arm64 build (Homebrew in /opt/homebrew)",
					c.Title+" is the Intel version, so it runs slower on this Mac", "Install the Apple silicon version with Homebrew.")
				allOK = false
				continue
			}
			add("tools", t, "ok", detail, "")
		}
	}
	if allOK {
		add("tools", "video tools", "ok", "ffmpeg, ffprobe and node found", "", "Video tools are installed")
	}
	if ts.Has(tools.FFmpeg) {
		if ts.HasEncoder(ctx, "h264_videotoolbox") {
			add("tools", "hardware encoding", "ok", "h264_videotoolbox available", "")
		} else {
			add("tools", "hardware encoding", "info", "software x264 only", "")
		}
	}
	// Optional: poppler turns PDF pages into pictures a cut can show (resources).
	if pdf, _ := components.Get("pdf-pages"); e.Find(ctx, pdf).Ready {
		add("tools", "PDF pages", "ok", "poppler "+ts.Path(tools.Pdftoppm), "")
	} else {
		add("tools", "PDF pages", "info", "poppler not installed: PDFs are kept, but their pages can't be shown in a video", "metarush components add pdf-pages",
			"PDF pages can't be shown in a video yet (PDFs are still kept)", "Add PDF pages in Settings → Components (optional).")
	}
	if p := transcribe.Select(cfg.Transcription, ts); p != nil {
		detail := p.Name() + " " + p.Model() + " (local, no cloud upload)"
		fix, next := "", ""
		if m, ok := p.(transcribe.Moder); ok {
			if m.Mode() == transcribe.ModeResident {
				detail += "; the model stays loaded between clips"
			} else {
				unit := "batch"
				if p.Name() == "whisper.cpp" {
					unit = "clip"
				}
				detail += "; the model loads again for every " + unit
				if why := transcribe.WhyNotResident(p); why != "" {
					detail += " (" + why + ")"
				}
				// whisper.cpp keeps its model loaded in whisper-server from the same build (D121).
				if p.Name() == "whisper.cpp" && cfg.Transcription.KeepModelLoaded() && !ts.Has(tools.WhisperServer) {
					fix = "build whisper.cpp's whisper-server and put it next to " + ts.Path(tools.Whisper)
					next = "Transcripts would come faster with whisper-server from the same whisper.cpp build next to whisper-cli (it keeps the speech model loaded between clips)."
				}
			}
		}
		add("tools", "transcription", "ok", detail, fix, "Transcripts are made on this machine; nothing is uploaded", next)
	} else {
		c, _ := components.Get("transcription")
		add("tools", "transcription", "warn", "no local transcriber — captions and transcript search unavailable", "metarush components add transcription",
			"Transcripts can't be made on this machine yet, so there are no captions or transcript search ("+c.Size()+" to add)", "Add transcription in Settings → Components.")
	}
}

// componentChecks says how many optional components are here, and lists
// the ones that aren't in --details (each with the command that adds it).
func componentChecks(ctx context.Context, cfg *config.Config, ts *tools.Set, add addCheck) {
	e := componentsEnv(cfg, ts)
	ready, optional := 0, 0
	var missing []string
	for _, st := range e.States(ctx) {
		if st.Required {
			if !st.Ready {
				add("components", st.Name, "fail", firstNonEmpty(st.Detail, "missing"), "metarush setup", st.Says, "Run MetaRush's setup again.")
			}
			continue
		}
		if st.Status == components.StatusNotHere {
			continue
		}
		optional++
		if st.Ready {
			ready++
		} else {
			missing = append(missing, st.Name)
		}
	}
	detail := fmt.Sprintf("%d of %d optional components here", ready, optional)
	if len(missing) > 0 {
		detail += "; not here: " + strings.Join(missing, ", ")
	}
	say := fmt.Sprintf("%d of %d optional components are on this computer", ready, optional)
	if len(missing) == 0 {
		add("components", "optional", "ok", detail, "", say)
		return
	}
	add("components", "optional", "info", detail, "metarush components", say, "Settings → Components says what each one unlocks and adds it.")
}

// activeWorkers describes the machines running MetaRush in the background:
// technically (name and busy slots) and in plain words.
func activeWorkers(ctx context.Context, a *app.App) (string, string, error) {
	var ws []models.WorkerDevice
	if err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), bson.M{"lastHeartbeat": bson.M{"$gt": time.Now().Add(-45 * time.Second)}}, &ws); err != nil {
		return "", "", err
	}
	if len(ws) == 0 {
		return "none online (jobs wait until a worker starts; CLI --wait runs one inline)", "No machine is running MetaRush in the background right now; work waits until one is", nil
	}
	var names, plain []string
	for _, w := range ws {
		names = append(names, fmt.Sprintf("%s (%d/%d busy)", w.Name, w.Running, w.Concurrency))
		state := "ready for work"
		if w.Running > 0 {
			state = plural(w.Running, "job") + " running"
		}
		plain = append(plain, fmt.Sprintf("%s (%s)", w.Name, state))
	}
	return strings.Join(names, ", "), "Running in the background on " + strings.Join(plain, ", "), nil
}

// localDatabaseChecks checks the database on this computer (D254): set up,
// running (doctor starts it when it isn't, as any command needing it would),
// and whether a newer version waits for a person to upgrade it.
func localDatabaseChecks(ctx context.Context, cfg *config.Config, l *config.LocalDatabase, ts *tools.Set, add addCheck) {
	srv := localdb.New(cfg.Home(), l.Port, ts)
	st := srv.Status(ctx)
	switch {
	case !st.SetUp:
		add("database", "on this computer", "fail", "not set up in "+st.Folder, "run `metarush setup`",
			"The database on this computer isn't set up", "Run MetaRush's setup again.")
		return
	case !st.Running:
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := srv.Start(sctx)
		cancel()
		if err != nil {
			add("database", "on this computer", "fail", fmt.Sprintf("MongoDB %s on 127.0.0.1:%d didn't start: %v (log %s)", st.Version, st.Port, err, st.Log), "metarush database start --details",
				"The database on this computer isn't running, and it didn't start: "+strings.TrimSuffix(err.Error(), "."), "Its log says why.")
			return
		}
		add("database", "on this computer", "ok", fmt.Sprintf("MongoDB %s on 127.0.0.1:%d, started by doctor (%s)", st.Version, st.Port, st.Folder), "",
			"The database on this computer wasn't running, so MetaRush started it")
	default:
		add("database", "on this computer", "ok", fmt.Sprintf("MongoDB %s on 127.0.0.1:%d, pid %d, %s of data (%s)", st.Version, st.Port, st.PID, fmtBytes(st.DataBytes), st.Folder), "",
			"Your projects are kept in a database on this computer")
	}
	if w := srv.PlaceWarning(); w != "" {
		add("database", "folder", "warn", "inside the git repository at the home folder (a dotfiles repository): git clean -fdX would delete "+st.Folder,
			"leave MetaRush's folder out of git clean, or move it: metarush setup --home <a folder outside the repository>", strings.TrimSuffix(w, "."))
	}
	// It must never answer without a password (D254): other programs and accounts on this computer can reach 127.0.0.1.
	if srv.RequiresSignIn(ctx) {
		add("database", "sign-in required", "ok", "connections without a sign-in are refused (--auth, key file)", "")
	} else {
		add("database", "sign-in required", "fail", "the server answered a connection without a sign-in", "metarush stop && metarush database start; if it persists, set the home up again",
			"The database on this computer answers without a password", "Stop MetaRush and start it again; if it still does, set MetaRush up again.")
	}
	if st.UpgradeUnfinished {
		add("database", "upgrade", "warn", "the last upgrade stopped before the feature compatibility version moved on", "metarush stop && metarush database upgrade",
			"The database's last upgrade didn't finish", "Upgrade it again to finish it (MetaRush stops for a moment).")
	} else if st.UpgradeReady {
		add("database", "upgrade", "info", fmt.Sprintf("MongoDB %s is installed; this MetaRush comes with %s", st.Version, st.Pinned), "metarush stop && metarush database upgrade",
			"A newer version of the database comes with this MetaRush", "Upgrade it when it suits you (MetaRush stops for a minute).")
	}
}

// claudeChecks says whether Claude Code is installed and signed in (D255),
// as Claude Code itself answers: MetaRush never signs in for anyone and never
// reads its credentials. Without it MetaRush still works; with it, people
// edit by asking.
func claudeChecks(ctx context.Context, ts *tools.Set, add addCheck) {
	c := firstrun.CheckClaude(ctx, ts)
	detail := "not installed"
	if c.Installed {
		detail = "Claude Code " + orWord(c.Version, "(version unknown)")
		switch {
		case c.SignedIn == nil:
			detail += ", sign-in not reported"
		case *c.SignedIn:
			detail += ", signed in (" + c.Billing + ")"
		default:
			detail += ", not signed in"
		}
	}
	status := "ok"
	switch {
	case !c.Installed:
		status = "info"
	case !c.Ready:
		status = "warn"
	}
	add("claude code", "claude code", status, detail, c.Command, strings.TrimSuffix(c.Says, "."), c.Next)
}

// envSecretsCheck: with secrets in the home's file, each one config.yaml
// names should be in <home>/.env. One only in this terminal's environment is
// missing for MetaRush in the background and for the Claude Code panel's
// metarush, which never gets MetaRush's secrets from the environment.
func envSecretsCheck(h string, cfg *config.Config, add addCheck) {
	if cfg.SecretMode() != "env" {
		return
	}
	names := []string{config.EnvPrefix + "MONGODB_URI"}
	if cfg.Media.Provider == "s3" {
		names = append(names, config.EnvPrefix+"S3_SECRET_ACCESS_KEY")
	}
	var only []string
	for _, n := range names {
		if _, inFile := config.DotEnvValue(h, n); !inFile && os.Getenv(n) != "" {
			only = append(only, n)
		}
	}
	if len(only) == 0 {
		return
	}
	add("security", "secrets file", "warn", strings.Join(only, ", ")+" set only in this process's environment, not in "+config.DotEnvPath(h),
		"metarush init --reconfigure --secrets env (writes them into the home's .env)",
		"Some of MetaRush's passwords are only in this terminal's settings, so MetaRush in the background and the Claude Code panel can't find them",
		"Keep them in MetaRush's secrets file: set MetaRush up again with the same settings.")
}
