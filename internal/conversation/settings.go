package conversation

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

func init() {
	ops.Def("conversation.settings", "Change how a cut plays a conversation: each person's microphone quieter while they aren't speaking (and by how much), name tags in layouts, and a person's crop (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallSettingsIn) (*CallPlanOut, error) {
			return settings(ctx, a, in)
		})
}

// CallSettingsIn changes a cut's conversation settings.
type CallSettingsIn struct {
	Project string `json:"project" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Branch  string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	// Conversation is the conversation in the cut: its short name there, its
	// name or ID (default: the cut's only one).
	Conversation string `json:"conversation,omitempty"`
	// Quieter: each microphone quieter while its person isn't speaking (what
	// keeps one person's room and the other's voice out of it); Depth how much (dB).
	Quieter *bool    `json:"quieter,omitempty"`
	Depth   *float64 `json:"depth,omitempty" jsonschema:"minimum=6,maximum=40"`
	// NameTags: names on the tiles of side-by-side, stacked, picture-in-picture and grid shots.
	NameTags *bool `json:"nameTags,omitempty"`
	// Member and Crop: cut the edges of one person's picture (fractions of
	// each side, 0–0.4); NoCrop takes the crop away.
	Member string         `json:"member,omitempty"`
	Crop   *editspec.Crop `json:"crop,omitempty"`
	NoCrop bool           `json:"noCrop,omitempty"`
	Apply  bool           `json:"apply,omitempty"`
}

func settings(ctx context.Context, a *app.App, in *CallSettingsIn) (*CallPlanOut, error) {
	rev, err := latest(ctx, a, in.Project, in.Branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, invalid("the project has no cut yet")
	}
	names := sortedNames(rev.Spec.Conversations)
	name := ""
	switch {
	case len(names) == 0:
		return nil, invalid("the cut doesn't play a conversation")
	case in.Conversation == "" && len(names) == 1:
		name = names[0]
	case in.Conversation == "":
		return nil, invalid("the cut plays %d conversations: say which (%s)", len(names), strings.Join(names, ", "))
	default:
		for _, n := range names {
			if n == in.Conversation || rev.Spec.Conversations[n].ID == in.Conversation {
				name = n
			}
		}
		if name == "" {
			if c, err := load(ctx, a, in.Conversation, rev.ProjectID); err == nil {
				for _, n := range names {
					if rev.Spec.Conversations[n].ID == c.ID {
						name = n
					}
				}
			}
		}
		if name == "" {
			return nil, invalid("the cut doesn't play %q (it plays %s)", in.Conversation, strings.Join(names, ", "))
		}
	}
	pin := rev.Spec.Conversations[name]
	var changes []string
	if in.Quieter != nil || in.Depth != nil {
		on := pin.Gate != nil && pin.Gate.Mode == editspec.GateSpeaking
		if in.Quieter != nil {
			on = *in.Quieter
		}
		if !on {
			pin.Gate = nil
			changes = append(changes, "every microphone plays at its level all the time")
		} else {
			g := editspec.NewGate()
			if pin.Gate != nil && pin.Gate.DepthDB != nil {
				d := *pin.Gate.DepthDB
				g.DepthDB = &d
			}
			if in.Depth != nil {
				d := math.Round(*in.Depth*10) / 10
				g.DepthDB = &d
			}
			pin.Gate = g
			changes = append(changes, fmt.Sprintf("each microphone %.0f dB quieter while its person isn't speaking", pin.GateDepth()))
		}
	}
	if in.NameTags != nil {
		if *in.NameTags {
			pin.NameTags = ""
			changes = append(changes, "name tags in layouts")
		} else {
			pin.NameTags = "off"
			changes = append(changes, "no name tags in layouts")
		}
	}
	if in.Member != "" || in.Crop != nil || in.NoCrop {
		if in.Member == "" {
			return nil, invalid("say whose picture to crop (member)")
		}
		k := -1
		for i, m := range pin.Members {
			if m.ID == in.Member || strings.EqualFold(m.Name, in.Member) {
				k = i
			}
		}
		if k < 0 {
			return nil, invalid("there's no member %q in the cut's conversation", in.Member)
		}
		switch {
		case in.NoCrop:
			pin.Members[k].Crop = nil
			changes = append(changes, possessive(pin.Members[k].Name)+" whole picture")
		case in.Crop != nil:
			cr := *in.Crop
			for _, v := range []float64{cr.Left, cr.Top, cr.Right, cr.Bottom} {
				if v < 0 || v > 0.4 {
					return nil, invalid("a crop takes 0 to 0.4 of each side")
				}
			}
			pin.Members[k].Crop = &cr
			changes = append(changes, fmt.Sprintf("%s picture cropped (left %.0f%%, top %.0f%%, right %.0f%%, bottom %.0f%%)", possessive(pin.Members[k].Name), cr.Left*100, cr.Top*100, cr.Right*100, cr.Bottom*100))
		default:
			return nil, invalid("say the crop (crop) or take it away (noCrop)")
		}
	}
	if len(changes) == 0 {
		return nil, invalid("nothing to change: say quieter, depth, nameTags or a member's crop")
	}
	p := pin
	res, err := planCheck(ctx, a, rev, "Conversation settings: "+strings.Join(changes, "; "), []editspec.Op{{Op: "setConversation", ID: name, Conversation: &p}})
	if err != nil {
		return nil, err
	}
	out := &CallPlanOut{Changes: changes, Result: res, Summary: "Ready: " + strings.Join(changes, "; ") + "."}
	if in.Apply {
		out.Applied = applyResult(ctx, a, res)
		out.Summary = settle(out.Summary, out.Applied)
	}
	return out, nil
}
