package firstrun

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// SetupStatusIn asks what works on this computer (the dashboard's first run,
// `metarush setup status`). Refresh asks Claude Code again instead of using
// the answer kept for a minute.
type SetupStatusIn struct {
	Refresh bool `json:"refresh,omitempty"`
}

// SetupStatusOut is what MetaRush can do on this computer (D256): what works
// with no keys, what a key adds, Claude Code (D255), and where the projects
// are kept.
type SetupStatusOut struct {
	WhatWorks
	// FirstRun: the workspace has no projects and no footage yet, so the
	// dashboard opens with this (Home's Getting started).
	FirstRun bool `json:"firstRun"`
	// Database says where the projects are kept.
	Database SetupDatabase `json:"database"`
	// Machine is this computer's name (things above are about it).
	Machine string `json:"machine"`
	// Components are the optional components not on this computer yet, the
	// suggested ones first: Getting started offers Add it for each (D324).
	Components []components.ComponentState `json:"components"`
}

// SetupDatabase is where the projects are kept, in plain words.
type SetupDatabase struct {
	// Local: the database on this computer (D254).
	Local        bool   `json:"local"`
	Running      bool   `json:"running"`
	UpgradeReady bool   `json:"upgradeReady"`
	Says         string `json:"says"`
}

func init() {
	ops.Def("setup.status", "Say what works on this computer: free with no keys, what a key adds, Claude Code's sign-in and where the projects are kept", ops.Flags{},
		func(ctx context.Context, a *app.App, in *SetupStatusIn) (*SetupStatusOut, error) {
			if in.Refresh {
				ForgetClaude()
			}
			out := &SetupStatusOut{WhatWorks: Build(ctx, a.Cfg, a.Tools), Database: DatabaseState(ctx, a.Cfg, a.Tools), Machine: a.Cfg.Installation.Name,
				Components: MissingComponents(ctx, components.For(a))}
			projects, err := a.DB.C(store.CProjects).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
			if err != nil {
				return nil, err
			}
			assets, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
			if err != nil {
				return nil, err
			}
			out.FirstRun = projects == 0 && assets == 0
			return out, nil
		})
}

// MissingComponents are the optional components that could be on this
// computer and aren't, the suggested ones first.
func MissingComponents(ctx context.Context, e *components.Env) []components.ComponentState {
	var first, rest []components.ComponentState
	for _, st := range e.States(ctx) {
		if st.Required || st.Ready || st.Status == components.StatusNotHere {
			continue
		}
		if st.Suggested {
			first = append(first, st)
		} else {
			rest = append(rest, st)
		}
	}
	return append(append([]components.ComponentState{}, first...), rest...)
}

// DatabaseState says where the projects are kept and how that database is.
func DatabaseState(ctx context.Context, cfg *config.Config, ts *tools.Set) SetupDatabase {
	l := cfg.Database.Local
	if l == nil {
		d := SetupDatabase{Running: true, Says: "Your projects are kept in a database elsewhere, which your other computers can share."}
		if strings.Contains(config.HostOf(cfg.Database.URI), ".mongodb.net") {
			d.Says = "Your projects are kept in a database on MongoDB Atlas, which your other computers can share."
		}
		return d
	}
	st := localdb.New(cfg.Home(), l.Port, ts).Status(ctx)
	d := SetupDatabase{Local: true, Running: st.Running, UpgradeReady: st.UpgradeReady,
		Says: "Your projects are kept in a database on this computer; MetaRush keeps it running."}
	if !st.Running {
		d.Says = "Your projects are kept in a database on this computer. It isn't running right now; MetaRush starts it when it needs it."
	}
	return d
}
