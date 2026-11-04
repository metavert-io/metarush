package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/tools"
)

// SetupQuestions is `metarush setup questions --json`: what setup would ask on
// this computer, with its suggested answers, changing nothing (D473). The Mac
// app's first-run panels show it and pass the answers back to setup as flags,
// so no question or suggestion is written twice; an agent can read it before
// it runs setup.
type SetupQuestions struct {
	Home string `json:"home"`
	// SetUp: MetaRush is set up in this home already. Setup then runs again
	// and keeps where things are kept, so only the port, the background
	// service, the skills and the components are asked.
	SetUp      bool                     `json:"setUp"`
	Storage    SetupStorageQuestion     `json:"storage"`
	Database   SetupDatabaseQuestion    `json:"database"`
	Port       int                      `json:"port"` // the dashboard's port: suggested, or the one in use
	Service    SetupServiceQuestion     `json:"service"`
	Skills     SetupSkillsQuestion      `json:"skills"`
	Components []SetupComponentQuestion `json:"components"`
}

// SetupStorageQuestion is where footage and finished videos are kept.
type SetupStorageQuestion struct {
	// Suggested is folder (a folder on this computer); s3 and gcs are the
	// advanced answers, which need a bucket's key typed at a terminal.
	Suggested string `json:"suggested" jsonschema:"enum=folder,enum=s3,enum=gcs"`
	// Folder is the suggested folder (or, set up, the one in use).
	Folder string `json:"folder,omitempty"`
	Says   string `json:"says"`
}

// SetupDatabaseQuestion is where the projects are kept.
type SetupDatabaseQuestion struct {
	Suggested string `json:"suggested" jsonschema:"enum=here,enum=elsewhere"`
	// Here: MetaRush can set up a database on this computer.
	Here       bool `json:"here"`
	DownloadMB int  `json:"downloadMB,omitempty"`
	// Note is said before anything downloads (a Linux kernel that stops MongoDB 8).
	Note string `json:"note,omitempty"`
	Says string `json:"says"`
}

// SetupServiceQuestion is MetaRush starting when the person logs in.
type SetupServiceQuestion struct {
	// Suggested: start MetaRush when the person logs in. False when another
	// copy of MetaRush runs the service already: whether this copy takes its
	// place is a question of its own (`setup --take-over-service`), and its
	// answer is no unless a person says yes (D473).
	Suggested bool `json:"suggested"`
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	// WorkerOnly: the service runs the background work without the dashboard,
	// and setting it up again keeps it so.
	WorkerOnly bool `json:"workerOnly,omitempty"`
	// OtherCopy: the background service starts another copy of MetaRush than
	// this one (`service status`'s otherCopy); OtherVersion is the version
	// that copy runs, when it says, and Newer that it is newer than this one,
	// so setup never takes its place.
	OtherCopy    bool   `json:"otherCopy,omitempty"`
	OtherVersion string `json:"otherVersion,omitempty"`
	Newer        bool   `json:"newer,omitempty"`
	// VersionUnknown: that copy doesn't say which version it is (or one this
	// copy can't order against its own), so it may be newer: setup leaves it
	// as it is, as it does a newer one.
	VersionUnknown bool   `json:"versionUnknown,omitempty"`
	Says           string `json:"says"`
}

// SetupSkillsQuestion is Claude Code learning MetaRush's video skills.
type SetupSkillsQuestion struct {
	Suggested       bool   `json:"suggested"`
	ClaudeInstalled bool   `json:"claudeInstalled"`
	Says            string `json:"says"`
}

// SetupComponentQuestion is one row of the components checklist (D324).
type SetupComponentQuestion struct {
	components.ComponentState
	// Ticked: setup ticks it for a person answering the checklist.
	Ticked bool `json:"ticked"`
	// DownloadMB and Minutes add up to what the ticked ones take.
	DownloadMB int `json:"downloadMB"`
	Minutes    int `json:"minutes"`
}

func setupQuestionsCmd() *cobra.Command {
	return &cobra.Command{Use: "questions", Short: "Say what setup would ask on this computer and its suggested answers, changing nothing",
		Long: "What metarush setup asks here, each question with its suggested answer and the optional components it ticks, so an app or\n" +
			"an agent can show them and pass the answers back to setup as flags. Nothing is set up, downloaded or started.",
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return failErr(c, err)
			}
			q := setupQuestions(c, h)
			return emit(c, q, func() { printSetupQuestions(c, q) })
		}}
}

func setupQuestions(c *cobra.Command, h string) *SetupQuestions {
	ctx := c.Context()
	q := &SetupQuestions{Home: h, Components: []SetupComponentQuestion{}}
	cfg := config.Default(h)
	if config.Exists(h) {
		if loaded, err := config.Load(h); err == nil {
			cfg, q.SetUp = loaded, true
		}
	}
	ts := tools.New(cfg.Tools)
	if q.SetUp {
		st := setupStorageOut(cfg)
		q.Storage = SetupStorageQuestion{Suggested: st.Kind, Folder: st.Folder, Says: st.Says}
		db := firstrun.DatabaseState(ctx, cfg, ts)
		q.Database = SetupDatabaseQuestion{Suggested: map[bool]string{true: "here", false: "elsewhere"}[db.Local], Here: db.Local, Says: db.Says}
		q.Port = cfg.Dashboard.Port
	} else {
		q.Storage = SetupStorageQuestion{Suggested: "folder", Folder: filepath.Join(h, "media"),
			Says: "Footage, preview copies and finished videos are kept in a folder on this computer; a cloud storage bucket your team shares is the advanced choice."}
		srv := localdb.New(h, 0, ts)
		d := SetupDatabaseQuestion{Here: srv.Available(), Note: srv.KernelNote()}
		switch {
		case !d.Here:
			d.Suggested = "elsewhere"
			d.Says = "MongoDB doesn't make a database for this kind of computer, so MetaRush needs one you already have (MongoDB Atlas has a free tier)."
		case os.Getenv(config.EnvPrefix+"MONGODB_URI") != "":
			d.Suggested, d.DownloadMB = "elsewhere", srv.DownloadMB()
			d.Says = "This terminal names a database for MetaRush, so setup uses that one."
		default:
			d.Suggested, d.DownloadMB = "here", srv.DownloadMB()
			d.Says = fmt.Sprintf("Your projects are kept in a database on this computer: MetaRush downloads it from MongoDB (about %d MB, free) and looks after it.", d.DownloadMB)
		}
		q.Database = d
		q.Port = suggestedPort()
	}

	q.Service = SetupServiceQuestion{Suggested: true, Says: "MetaRush starts when you log in and keeps running, so the dashboard is always there."}
	if exe, err := service.StableExecutable(); err == nil {
		st := service.Options{Home: h, Exe: exe, Tools: ts}.Get(ctx)
		q.Service.Installed, q.Service.Running, q.Service.WorkerOnly = st.Installed, st.Running, st.Installed && st.WorkerOnly
		if st.Installed && st.OtherCopy {
			var loaded *config.Config
			if q.SetUp {
				loaded = cfg
			}
			other := otherCopyOf(ctx, loaded, st)
			q.Service.OtherCopy, q.Service.Suggested = true, false
			q.Service.OtherVersion, q.Service.Newer, q.Service.VersionUnknown = other.Version, other.Newer, !other.Newer && !other.Known
			switch {
			case other.Newer:
				q.Service.Says = "MetaRush already starts when you log in, from another copy that is newer than this one" + other.said() + ", so setup leaves it as it is."
			case !other.Known:
				q.Service.Says = otherUnknownSays
			default:
				q.Service.Says = "MetaRush already starts when you log in, from another copy of MetaRush" + other.said() + "; setup leaves it as it is unless you choose to have it start this copy instead."
			}
		}
	}

	claude := firstrun.CheckClaude(ctx, ts)
	q.Skills = SetupSkillsQuestion{Suggested: claude.Installed, ClaudeInstalled: claude.Installed,
		Says: "Claude Code learns MetaRush's video skills, so you can edit by asking."}
	if !claude.Installed {
		q.Skills.Says = "Claude Code isn't installed, so its video skills are left out; run setup again once it is."
	}

	e := componentsEnv(cfg, ts)
	offered := e.Optional()
	ticked := suggestedComponents(e, offered, q.SetUp, true)
	for _, comp := range offered {
		if f := e.Find(ctx, comp); f.NotHere != "" {
			continue
		}
		q.Components = append(q.Components, SetupComponentQuestion{ComponentState: e.StateOf(ctx, comp),
			Ticked: slices.Contains(ticked, comp.Name), DownloadMB: comp.DownloadMB, Minutes: comp.Minutes})
	}
	return q
}

func printSetupQuestions(c *cobra.Command, q *SetupQuestions) {
	if q.SetUp {
		fmt.Println("MetaRush is set up on this computer already: setup checks that everything is in place and keeps where things are kept.")
	} else {
		fmt.Println("What setup asks on this computer, with its suggested answers:")
	}
	fmt.Println("  · " + q.Storage.Says)
	fmt.Println("  · " + q.Database.Says)
	if q.Database.Note != "" {
		fmt.Println("    " + q.Database.Note)
	}
	fmt.Println("  · " + q.Service.Says)
	fmt.Println("  · " + q.Skills.Says)
	n := 0
	for _, x := range q.Components {
		if x.Ticked && !x.Ready {
			n++
		}
	}
	fmt.Printf("  · %s ticked among the optional components.\n", plural(n, "component"))
	if detailsMode(c) {
		fmt.Printf("home %s, dashboard port %d\n", q.Home, q.Port)
		for _, x := range q.Components {
			mark := "[ ]"
			switch {
			case x.Ready:
				mark = " ✓ "
			case x.Ticked:
				mark = "[x]"
			}
			fmt.Printf("  %s %s (%s)\n", mark, x.Name, x.Size)
		}
	} else {
		fmt.Println("(Add --details to see the components one by one, the folder and the port.)")
	}
}
