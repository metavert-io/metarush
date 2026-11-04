package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/ops"
)

// Background removal for video (D505–D514) in the panel: Claude's refused
// `metarush cut background …` is an editing verb's card (cutedits.go: checked
// when it is made, saved by the person's click), and `metarush footage
// cut-out <clip>` a card that starts the clip's cut-out. Either runs by
// itself under Auto only when a computer of the workspace makes the cut-out
// (automode.go): a clip never leaves for a service elsewhere without a person.

// backgroundInput reads `metarush cut background <project> --clip <clip>
// [--with …] [--colour …] [--blur N] [--picture …] [--footage … --footage-at
// …] [--edges N] [--off] [--model …]` the way the CLI does. Claude's
// --max-cost is never taken: a price is the person's to agree to.
func backgroundInput(c ops.CutEditCommon, call *cliCall) (any, bool) {
	in := mattes.CutBackgroundIn{CutEditCommon: c, Clip: call.Flags["clip"], With: call.Flags["with"], Colour: call.Flags["colour"],
		Picture: call.Flags["picture"], Footage: call.Flags["footage"], Off: hasBool(call, "off"), Model: call.Flags["model"]}
	if in.Clip == "" && len(call.Args) > 1 {
		in.Clip = call.Args[1]
	}
	if in.Clip == "" {
		return nil, false
	}
	if v := call.Flags["blur"]; v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, false
		}
		in.Blur = n
	}
	if v := call.Flags["edges"]; v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, false
		}
		in.Feather = &n
	}
	if v := call.Flags["footage-at"]; v != "" {
		n, ok := clockOf(v)
		if !ok {
			return nil, false
		}
		in.FootageAt = n
	}
	return in, true
}

// clockOf reads 0:03.5 or 3.5.
func clockOf(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if m, sec, ok := strings.Cut(s, ":"); ok {
		mm, err := strconv.Atoi(m)
		ss, err2 := strconv.ParseFloat(sec, 64)
		return float64(mm)*60 + ss, err == nil && err2 == nil && mm >= 0 && ss >= 0
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && v >= 0
}

// describeFootageCutout is the card that starts a clip's cut-out: where it
// would be made, with what and how long it takes (a dry run now).
func (m *Manager) describeFootageCutout(ctx context.Context, call *cliCall, a *AssistantAction) {
	ref := first(call.Args)
	if ref == "" {
		incomplete(a, "Cut the people out of a clip", "Claude did not name the clip.")
		return
	}
	in := mattes.CutoutIn{AssetID: ref, ProjectID: call.Flags["project"], Model: call.Flags["model"], Fixed: call.Flags["fixed"]}
	if r := call.Flags["range"]; r != "" {
		from, to, ok := strings.Cut(r, "-")
		f, ok1 := clockOf(from)
		t, ok2 := clockOf(to)
		if !ok || !ok1 || !ok2 || t <= f {
			incomplete(a, "Cut the people out of a clip", "Claude gave a part of the clip MetaRush can't read.")
			return
		}
		in.From, in.To = &f, &t
	}
	for _, x := range []struct {
		flag string
		dst  **float64
	}{{"from", &in.From}, {"to", &in.To}} {
		if v := call.Flags[x.flag]; v != "" {
			n, ok := clockOf(v)
			if !ok {
				incomplete(a, "Cut the people out of a clip", "Claude gave a time MetaRush can't read.")
				return
			}
			*x.dst = &n
		}
	}
	dry := in
	dry.DryRun = true
	b, _ := json.Marshal(dry)
	res, err := ops.Call(ctx, m.App, "footage.cutout", b, ops.Caller{})
	if err != nil {
		incomplete(a, "Cut the people out of a clip", firstLine(apperr.From(err).Message))
		return
	}
	r := res.(*mattes.CutoutOut)
	a.Title = "Cut the people out of " + m.cardName(ctx, ref)
	a.Details = []string{r.Summary}
	if r.Reused || r.Matte.State == "ready" {
		incomplete(a, a.Title, r.Summary)
		return
	}
	a.Details = append(a.Details, "For background removal in video: cuts then put a colour, a blur, a picture or another clip behind them")
	setInput(a, "footage.cutout", in)
}

// cutoutWork is the cut-out a card started: Claude hears once it is made.
func cutoutWork(o *mattes.CutoutOut, projectID string) *ActionWork {
	if o == nil || o.DryRun || o.JobID == "" || o.Matte.State == "ready" {
		return nil
	}
	link := "/library"
	if projectID != "" {
		link = "/p/" + projectID + "?tab=cut"
	}
	return &ActionWork{Kind: WorkJobs, JobIDs: []string{o.JobID}, What: "the clip's cut-out", State: WorkRunning, StartedAt: time.Now().UTC(), Link: link}
}

// backgroundWork is a background card's work: the clip's cut-out it started.
func backgroundWork(o *mattes.CutBackgroundOut) *ActionWork {
	if o == nil || !o.Waiting {
		return nil
	}
	return cutoutWork(o.Cutout, o.ProjectID)
}
