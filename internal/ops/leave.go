package ops

import (
	"context"
	"slices"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// What may leave (D288–D290): internal/leave sets these, so places in this
// package that send a client's things off this computer ask its rules
// without importing it. Unset, everything is allowed (today's behaviour).

// LeaveCheck refuses sending kinds of things of these projects to a place
// (and receiver) their Content Settings keep them from (E_KEPT_HERE), and
// records the refusal.
var LeaveCheck func(ctx context.Context, a *app.App, place, to, toWords string, kinds, projects []string, what string) error

// LeaveStorage refuses moving the media to a store that is cloud storage
// while a client's rules keep their things from it.
var LeaveStorage func(ctx context.Context, a *app.App, to config.Media) error

// MayLeave asks LeaveCheck (allowed when nothing set it).
func MayLeave(ctx context.Context, a *app.App, place, to, toWords string, kinds, projects []string, what string) error {
	if LeaveCheck == nil {
		return nil
	}
	return LeaveCheck(ctx, a, place, to, toWords, kinds, projects, what)
}

// AIReads refuses an operation that would show an AI (Claude in the panel,
// Claude Code in a terminal, an AI app through MCP) a client's words or
// pictures their Content Settings keep on this computer; dispatch asks it
// with the decoded input.
var AIReads func(ctx context.Context, a *app.App, op string, in any) error

// deliveryMayLeave refuses writing a client's video, sound or words into a
// folder a sync service uploads to the cloud when their Content Settings
// keep them from delivery (D290). Other folders are on this computer.
func deliveryMayLeave(ctx context.Context, a *app.App, dir string, kinds, projects []string, what string) error {
	if v := secretpath.Inspect(dir); v.Kind != secretpath.KindSync {
		return nil
	}
	return MayLeave(ctx, a, models.PlaceDelivery, "sync", "a folder a sync service uploads to the cloud", kinds, projects, what)
}

// deliveryKinds are what a delivered video carries (its picture, sound and
// faces; captions and notes are words).
var deliveryKinds = []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeaveWords}

// LeaveSwitch refuses, for anyone but an owner, a change of which Content
// Settings a project uses that would let more of its things leave (D289):
// from the settings of fromOwner to those of toOwner (project IDs; "" none).
var LeaveSwitch func(ctx context.Context, a *app.App, projectName, fromOwner, toOwner string) error

func leaveSwitch(ctx context.Context, a *app.App, projectName, fromOwner, toOwner string) error {
	if fromOwner == toOwner {
		return nil
	}
	// The ship gate the same way (D299): settings that drop a gate that's on,
	// promises or house sign-offs are a person's who is an owner.
	if err := gateSwitch(ctx, a, projectName, fromOwner, toOwner); err != nil {
		return err
	}
	if LeaveSwitch == nil {
		return nil
	}
	return LeaveSwitch(ctx, a, projectName, fromOwner, toOwner)
}

// AIOutput lets internal/leave keep an AI's answer to what it may see (files
// in shared folders become copies in its conversation's own folder).
var AIOutput func(ctx context.Context, a *app.App, op string, out any) any

// LeaveCarry refuses putting a client's things where looser rules apply
// (moving a clip, linking a project) unless a person who is an owner does it
// (D289): before are the projects whose rules they follow now ("" the
// default's), after those they would follow, into the project they go to.
var LeaveCarry func(ctx context.Context, a *app.App, before, after []string, into, what string) error

func leaveCarry(ctx context.Context, a *app.App, before, after []string, into, what string) error {
	if LeaveCarry == nil {
		return nil
	}
	return LeaveCarry(ctx, a, before, after, into, what)
}

// assetProjects are the projects a clip counts under ("" none: the
// default's), the Trash's memberships too.
func assetProjects(as *models.Asset) []string {
	var out []string
	for _, p := range as.ProjectIDs {
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if as.Trash != nil {
		for _, p := range as.Trash.ProjectIDs {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// carryAsset asks LeaveCarry about a clip leaving one project (remove) and
// joining another (add).
func carryAsset(ctx context.Context, a *app.App, as *models.Asset, remove, add string) error {
	before := assetProjects(as)
	var after []string
	for _, p := range before {
		if p != "" && p != remove {
			after = append(after, p)
		}
	}
	if add != "" && !slices.Contains(after, add) {
		after = append(after, add)
	}
	if len(after) == 0 {
		after = []string{""}
	}
	return leaveCarry(ctx, a, before, after, add, "“"+as.DisplayTitle()+"”")
}

// RevisionTouches are the projects a version touches: its project and those
// of every clip, nested cut, conversation member and picture its cut plays
// (set by internal/leave; unset, its project only).
var RevisionTouches func(ctx context.Context, a *app.App, revisionID string) []string

func revisionTouches(ctx context.Context, a *app.App, rev *models.Revision) []string {
	if RevisionTouches == nil {
		return []string{rev.ProjectID}
	}
	return RevisionTouches(ctx, a, rev.ID)
}

// ReviewLinksOnMove ends the review links whose pages are kept in the
// storage the media is leaving (D318), reading their last comments there
// first; plan only says what would end. Set by internal/reviewlinks.
var ReviewLinksOnMove func(ctx context.Context, a *app.App, from media.Store, plan bool) ([]string, error)
