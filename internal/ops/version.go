package ops

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// VersionShowIn asks about this computer's MetaRush (Settings → General,
// `metarush version --details`).
type VersionShowIn struct{}

// VersionShowOut is this computer's MetaRush (D130): its version, what's new in
// it, and the MetaRush every computer in the workspace runs.
type VersionShowOut struct {
	version.Info
	// EditSpec is the cut format this version reads and writes.
	EditSpec int `json:"editSpec"`
	// WhatsNew is from the changelog built into this version: a release's own
	// notes; for a build past a release, the changes not released yet first
	// (Release "" in a section), then the release's notes.
	WhatsNew []version.ReleaseNotes `json:"whatsNew"`
	// Computers are the workspace's computers (people removed from it are left
	// out), this one first, each compared with this version.
	Computers []ComputerVersion `json:"computers"`
	// Older counts the computers that run an older MetaRush than this one.
	Older int `json:"older"`
	// ComputersError says why the computers couldn't be listed (the command
	// line without a workspace; the dashboard always has one).
	ComputersError string `json:"computersError,omitempty"`
}

// ComputerVersion is one computer in the workspace and the MetaRush it runs.
type ComputerVersion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
	// This is the computer answering: the one this dashboard or command runs on.
	This bool `json:"this"`
	// Version is the MetaRush it last ran (its daemon reports it every minute); Release is its release.
	Version string `json:"version,omitempty"`
	Release string `json:"release,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`
	// Standing compares its MetaRush with this one: older (it should be updated), newer, the same, or
	// unknown (it hasn't said yet, or a build from source of the same release).
	Standing   string     `json:"standing" jsonschema:"enum=same,enum=older,enum=newer,enum=unknown"`
	State      string     `json:"state,omitempty" jsonschema:"enum=invited,enum=active"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

func init() {
	Def("version.show", "Show this computer's MetaRush version, what's new in it and the version each computer in the workspace runs", Flags{},
		func(ctx context.Context, a *app.App, _ *VersionShowIn) (*VersionShowOut, error) {
			return VersionAbout(ctx, a)
		})
}

// VersionAbout is version.show; a nil app (the command line without a
// workspace) gives the version and what's new without the computers.
func VersionAbout(ctx context.Context, a *app.App) (*VersionShowOut, error) {
	b := version.Current()
	out := &VersionShowOut{Info: b, EditSpec: editspec.SchemaVersion, WhatsNew: version.WhatsNew(b), Computers: []ComputerVersion{}}
	if a == nil {
		return out, nil
	}
	var insts []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), nil, &insts, options.Find()); err != nil {
		return nil, err
	}
	for _, in := range insts {
		if in.State == models.InstallationRevoked {
			continue
		}
		c := ComputerVersion{ID: in.ID, Name: in.Name, Hostname: in.Hostname, OS: in.OS, Arch: in.Arch, This: in.ID == a.Cfg.Installation.ID,
			Version: in.Version, Commit: in.Commit, Built: in.Built, State: in.State}
		if c.State == "" {
			c.State = models.InstallationActive
		}
		if !in.LastSeenAt.IsZero() {
			t := in.LastSeenAt
			c.LastSeenAt = &t
		}
		if c.This {
			// What answers is this build, whatever the record last said.
			c.Version, c.Commit, c.Built = b.Version, b.Commit, b.Date
		}
		c.Standing = standing(c, b.Version)
		if c.Version != "" {
			c.Release = version.Describe(c.Version, c.Commit, c.Built).Release
		}
		if c.Standing == "older" {
			out.Older++
		}
		out.Computers = append(out.Computers, c)
	}
	sort.SliceStable(out.Computers, func(i, j int) bool {
		x, y := out.Computers[i], out.Computers[j]
		if x.This != y.This {
			return x.This
		}
		if (x.LastSeenAt == nil) != (y.LastSeenAt == nil) {
			return x.LastSeenAt != nil
		}
		return x.LastSeenAt != nil && x.LastSeenAt.After(*y.LastSeenAt)
	})
	return out, nil
}

func standing(c ComputerVersion, this string) string {
	if c.This {
		return "same"
	}
	if c.State == models.InstallationInvited || c.Version == "" {
		return "unknown"
	}
	switch n, ok := version.Compare(c.Version, this); {
	case !ok:
		return "unknown"
	case n < 0:
		return "older"
	case n > 0:
		return "newer"
	}
	return "same"
}
