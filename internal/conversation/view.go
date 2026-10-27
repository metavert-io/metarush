package conversation

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// CallInfo is a conversation in plain words, for the dashboard and the CLI.
type CallInfo struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	ProjectIDs []string         `json:"projectIds"`
	Length     float64          `json:"length" jsonschema:"description=Seconds on the conversation's clock."`
	Members    []CallMemberInfo `json:"members"`
	// Sure says how sure the alignment is: very sure, fairly sure or unsure.
	Sure       string  `json:"sure"`
	Confidence float64 `json:"confidence"`
	Reference  string  `json:"reference,omitempty" jsonschema:"description=The member the others were lined up with."`
	Method     string  `json:"method,omitempty"`
	// Source is where it was imported from (zoom, riverside, squadcast, folder).
	Source  string `json:"source,omitempty"`
	Folder  string `json:"folder,omitempty"`
	Style   string `json:"style,omitempty"`
	Layout  string `json:"layout,omitempty"`
	Cleanup struct {
		Open     int `json:"open"`
		Accepted int `json:"accepted"`
		Skipped  int `json:"skipped"`
	} `json:"cleanup"`
	// Suggestions are the clean-up suggestions found so far, with what people decided.
	Suggestions []CleanupInfo `json:"suggestions"`
	// Cuts are the projects' latest cuts that play it, and whether they use
	// its current alignment.
	Cuts []CallCutUse `json:"cuts"`
	Text string       `json:"text" jsonschema:"description=The conversation in a sentence."`
}

// CallMemberInfo is one recording in plain words.
type CallMemberInfo struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	AssetID    string  `json:"assetId"`
	Title      string  `json:"title" jsonschema:"description=The recording's title."`
	Use        string  `json:"use" jsonschema:"enum=both,enum=sound,enum=picture"`
	Start      float64 `json:"start"`
	Drift      float64 `json:"drift"`
	Duration   float64 `json:"duration"`
	Confidence float64 `json:"confidence"`
	Sure       string  `json:"sure"`
	Method     string  `json:"method,omitempty"`
	Delay      float64 `json:"delay,omitempty"`
	Nudged     float64 `json:"nudged,omitempty"`
	PersonID   string  `json:"personId,omitempty"`
	Tag        string  `json:"tag,omitempty"`
	HasPicture bool    `json:"hasPicture"`
	HasSound   bool    `json:"hasSound"`
	// Text says how it lines up ("starts 1.2 s in, lined up by sound, very sure").
	Text string `json:"text"`
	// Details is the measurement for --details (windows, spread, drift).
	Details string `json:"details,omitempty"`
	// Matched says how their webcam was matched to the reference, measured
	// ("colour difference 19 → 3.9, brightness 17 → 0"); "" when not matched.
	Matched string `json:"matched,omitempty"`
}

// CallCutUse is a cut that plays the conversation.
type CallCutUse struct {
	ProjectID  string `json:"projectId"`
	RevisionID string `json:"revisionId"`
	Number     int    `json:"number"`
	Name       string `json:"name" jsonschema:"description=The conversation's short name in that cut."`
	Current    bool   `json:"current" jsonschema:"description=The cut uses the conversation's current alignment."`
}

// Sure is a confidence in words.
func Sure(c float64) string {
	switch {
	case c >= 0.8:
		return "very sure"
	case c >= 0.5:
		return "fairly sure"
	}
	return "unsure"
}

// methodWords says how a member was lined up.
func methodWords(m string) string {
	switch m {
	case "reference":
		return "the others are lined up with it"
	case "mix":
		return "lined up by sound with the call's recording"
	case "two-way":
		return "lined up by sound (each microphone picked up the other)"
	case "one-way":
		return "lined up by sound (one microphone picked up the other, so it may be off by the call's delay)"
	case "together":
		return "taken to start with the others (nothing in their sound to line up on)"
	case "app":
		return "starts with the others, as the call app recorded it"
	case "clap":
		return "lined up on the clap"
	case "mark":
		return "lined up on the moments you marked"
	case "manual":
		return "placed by hand"
	case MethodClock:
		return "lined up by the clock it was recorded with"
	}
	return ""
}

// clockWords says a moment on the conversation's clock briefly ("1.2 s in").
func clockWords(t float64) string {
	switch {
	case math.Abs(t) < 0.0005:
		return "with the conversation"
	case t < 0:
		return fmt.Sprintf("%s before the conversation", secs(-t))
	}
	return fmt.Sprintf("%s in", secs(t))
}

func secs(t float64) string {
	if t >= 60 {
		return editspec.Clock(math.Round(t*10) / 10)
	}
	if t < 10 {
		return fmt.Sprintf("%.2g s", math.Round(t*100)/100)
	}
	return fmt.Sprintf("%.1f s", t)
}

func view(ctx context.Context, a *app.App, c *models.Conversation) CallInfo {
	as, _ := assets(ctx, a, c)
	v := CallInfo{ID: c.ID, Name: c.Name, ProjectIDs: c.ProjectIDs, Length: c.Length, Members: []CallMemberInfo{}, Style: c.Style, Layout: c.Layout, Cuts: []CallCutUse{}}
	if c.Source != nil {
		v.Source, v.Folder = c.Source.Provider, c.Source.Folder
	}
	if s := c.Sync; s != nil {
		v.Confidence, v.Reference, v.Method = s.Confidence, s.Reference, s.Method
	}
	v.Sure = Sure(v.Confidence)
	for _, m := range c.Members {
		mv := CallMemberInfo{ID: m.ID, Name: m.Name, AssetID: m.AssetID, Use: m.UseOf(), Start: m.Start, Drift: m.Drift, PersonID: m.PersonID, Tag: m.Tag,
			HasPicture: m.HasPicture(), HasSound: m.UseOf() != editspec.UsePicture}
		if x := as[m.AssetID]; x != nil {
			mv.Title, mv.Duration = x.DisplayTitle(), x.Media.Duration
			mv.HasPicture = mv.HasPicture && x.Media.HasVideo
			mv.HasSound = mv.HasSound && x.Media.HasAudio
		}
		parts := []string{"starts " + clockWords(m.Start)}
		if al := m.Align; al != nil {
			mv.Confidence, mv.Method, mv.Delay, mv.Nudged = al.Confidence, al.Method, al.Delay, al.Nudged
			mv.Sure = Sure(al.Confidence)
			if w := methodWords(al.Method); w != "" {
				parts = append(parts, w)
			}
			if al.Method != "reference" {
				parts = append(parts, mv.Sure)
			}
			if al.Nudged != 0 {
				parts = append(parts, fmt.Sprintf("moved %+.3f s by hand", al.Nudged))
			}
			mv.Details = fmt.Sprintf("start %.3f s · drift %+.1f ppm · %s · confidence %.2f", m.Start, m.Drift, al.Method, al.Confidence)
			if al.Windows > 0 {
				mv.Details += fmt.Sprintf(" · %d of %d windows agree (spread %.1f ms)", al.Agree, al.Windows, al.Residual)
			}
			if al.Delay != 0 {
				mv.Details += fmt.Sprintf(" · call delay %.3f s", al.Delay)
			}
		}
		if math.Abs(m.Drift) >= 1 {
			parts = append(parts, fmt.Sprintf("its clock runs %s (%s an hour)", map[bool]string{true: "fast", false: "slow"}[m.Drift > 0], secs(math.Abs(m.Drift)*3600/1e6)))
		}
		mv.Text = strings.Join(parts, ", ")
		if col := m.Color; col != nil && m.Look != nil {
			mv.Matched = fmt.Sprintf("colour difference %s → %s, brightness %s → %s", dE(col.DeltaEBefore), dE(col.DeltaEAfter), dE(col.LightBefore), dE(col.LightAfter))
		}
		v.Members = append(v.Members, mv)
	}
	v.Suggestions = []CleanupInfo{}
	for _, s := range c.Cleanup {
		v.Suggestions = append(v.Suggestions, cleanupInfo(c, s))
		switch s.State {
		case "accepted":
			v.Cleanup.Accepted++
		case "skipped":
			v.Cleanup.Skipped++
		default:
			v.Cleanup.Open++
		}
	}
	v.Cuts = cutsUsing(ctx, a, c)
	if v.Cuts == nil {
		v.Cuts = []CallCutUse{}
	}
	var people []string
	for _, m := range c.Members {
		if m.UseOf() != editspec.UsePicture {
			people = append(people, m.Name)
		}
	}
	v.Text = fmt.Sprintf("%s: %s, %s long, %d recordings in sync (%s).", c.Name, joinAnd(people), secs(c.Length), len(c.Members), v.Sure)
	if v.Sure == "unsure" {
		v.Text += " Check it in the preview and nudge a recording if someone's lips and voice don't match."
	}
	return v
}
