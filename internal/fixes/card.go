package fixes

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// The info card (D151): one card per source, built here so the dashboard, a
// terminal and Claude see the same thing.

// Card is a source's info card.
type Card struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// TitleBy: file | metarush | person ("" for older sources).
	TitleBy  string   `json:"titleBy,omitempty"`
	FileName string   `json:"fileName"`
	Aliases  []string `json:"aliases,omitempty"`
	// Suggestion is MetaRush's better name, when it has one.
	Suggestion *models.NameSuggestion `json:"suggestion,omitempty"`
	// NamedWords says who named it, in words ("MetaRush named it from its
	// first look", "You named it").
	NamedWords string `json:"namedWords"`
	Picture    bool   `json:"picture,omitempty"`
	// Origin is where it came from, Found what MetaRush found, in plain lines.
	Origin []string `json:"origin"`
	Found  []string `json:"found"`
	// People are the named voices and faces.
	People []string `json:"people,omitempty"`
	// OnScreen are words seen on screen.
	OnScreen []string `json:"onScreen,omitempty"`
	// Language is the language it is spoken in and how MetaRush knows (D243).
	Language *ops.FootageLanguageOut `json:"language,omitempty"`
	Topic    string                  `json:"topic,omitempty"`
	// Uses are where it is used (projects, collections, cuts and versions).
	Uses  []ops.UseRow `json:"uses"`
	Fixes *View        `json:"fixes,omitempty"`
	// Recorded is when it was recorded (from the file), Device and Software
	// what recorded it.
	Recorded *time.Time `json:"recorded,omitempty"`
	Device   string     `json:"device,omitempty"`
	Software string     `json:"software,omitempty"`
	Link     string     `json:"link,omitempty"`
	Summary  string     `json:"summary"`
}

func init() {
	ops.Def("asset.card", "Show a source's info card: its descriptive name and original file, where it came from, what MetaRush found in it (length, picture and sound, people, words on screen), where it is used and its fixes", ops.Flags{}, func(ctx context.Context, a *app.App, in *ShowIn) (*Card, error) {
		if strings.HasPrefix(in.ID, "res_") {
			return pictureCard(ctx, a, in.ID)
		}
		return card(ctx, a, in.ID)
	})
}

func card(ctx context.Context, a *app.App, id string) (*Card, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that clip.").WithDetails(map[string]any{"id": id})
	}
	c := &Card{ID: as.ID, Title: as.DisplayTitle(), TitleBy: as.TitleBy, FileName: as.Name, Suggestion: as.NameSuggestion, Origin: []string{}, Found: []string{}, Uses: []ops.UseRow{},
		Device: as.Media.Device, Software: as.Media.Software}
	for _, al := range as.Aliases {
		c.Aliases = append(c.Aliases, al.Name)
	}
	switch as.TitleBy {
	case models.TitleByPerson:
		c.NamedWords = "Named by a person."
	case models.TitleByMetaRush:
		c.NamedWords = "Named by MetaRush from its first look; rename it any time."
	default:
		c.NamedWords = "Named after its file."
	}
	// Where it came from.
	who := as.CreatedBy.UserName
	added := fmt.Sprintf("Added %s %s", as.CreatedAt.Local().Format("2 Jan 2006, 3:04 pm"), viaWords(as.Via))
	if who != "" {
		added += " by " + who
	}
	c.Origin = append(c.Origin, added+".")
	if src := as.Source; src != nil {
		switch {
		case src.IsGenerated():
			line := src.GeneratedLine()
			if g := src.Generated; g != nil && g.Prompt != "" {
				line += fmt.Sprintf(", from the words “%s”", g.Prompt)
			}
			c.Origin = append(c.Origin, line+".")
		case src.IsRemotion():
			c.Origin = append(c.Origin, src.RemotionLine()+".")
		default:
			line := "Imported " + strings.ToLower(src.Line()[:1]) + src.Line()[1:]
			if src.Title != "" {
				line += fmt.Sprintf(": “%s”", src.Title)
			}
			if src.License != "" {
				line += " (" + src.License + ")"
			}
			c.Origin = append(c.Origin, line+".")
			c.Link = src.URL
		}
	}
	if t, ok := recorded(&as); ok {
		c.Recorded = &t
		line := "Recorded " + t.Local().Format("2 Jan 2006, 3:04 pm")
		if as.Media.Device != "" {
			line += " on " + articled(as.Media.Device)
		}
		if as.Media.Software != "" {
			line += " (" + as.Media.Software + ")"
		}
		c.Origin = append(c.Origin, line+".")
	} else if as.Media.Device != "" {
		c.Origin = append(c.Origin, "Recorded on "+articled(as.Media.Device)+".")
	}
	// What MetaRush found.
	kind := kindOf(ctx, a, &as)
	c.Found = append(c.Found, lengthLine(&as, kind))
	if as.Media.HasAudio {
		c.Found = append(c.Found, soundLine(ctx, a, &as))
	}
	if people := peopleLine(ctx, a, &as); people != "" {
		c.Found = append(c.Found, people)
	}
	if as.Media.HasVideo {
		if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil && st.Index != nil {
			c.OnScreen = screenWords(st.Index)
			if s := strings.TrimSpace(st.Index.Summary); s != "" {
				c.Found = append(c.Found, "On screen: "+strings.TrimSuffix(s, ".")+".")
			}
			if len(c.OnScreen) > 0 {
				c.Found = append(c.Found, "Words on screen: “"+strings.Join(c.OnScreen, "”, “")+"”.")
			}
		}
	}
	if as.Transcript != nil && as.Transcript.State == "available" {
		var segs []models.TranscriptSegment
		_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}).SetProjection(bson.M{"text": 1}).SetLimit(4000))
		var texts []string
		for _, s := range segs {
			texts = append(texts, s.Text)
		}
		if c.Topic = sources.Topic(texts); c.Topic != "" {
			c.Found = append(c.Found, "What it's about: "+c.Topic+" (said most).")
		}
		c.Found = append(c.Found, fmt.Sprintf("Transcript: %d words.", as.Transcript.Words))
		if l := ops.LanguageOf(&as); l.Name != "" {
			c.Language = l
			c.Found = append(c.Found, "Spoken in "+l.Name+".")
		}
	} else if as.Media.HasAudio && as.Transcript != nil && as.Transcript.State == "running" {
		c.Found = append(c.Found, "The transcript is being made.")
	}
	if as.Fixes != nil {
		for _, m := range as.Fixes.Marks {
			if m.Measured != "" {
				f, _ := models.Fix(m.Fix)
				c.Found = append(c.Found, capFirst(m.Measured)+" ("+strings.ToLower(f.Label)+").")
			}
		}
	}
	c.People = sourcesPeople(ctx, a, as.ID)
	// Where it's used.
	if w, err := ops.CallTyped[ops.AssetWhereOut](ctx, a, "asset.where", ops.AssetRefIn{AssetID: as.ID}, ops.Caller{}); err == nil {
		c.Uses = w.Uses
	}
	v, err := view(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	c.Fixes = v
	c.Summary = fmt.Sprintf("“%s”: %s", c.Title, strings.TrimSuffix(c.Found[0], "."))
	return c, nil
}

func pictureCard(ctx context.Context, a *app.App, id string) (*Card, error) {
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that picture.")
	}
	v := r.Version(r.Current)
	c := &Card{ID: r.ID, Title: r.Title, Picture: true, Origin: []string{}, Found: []string{}, Uses: []ops.UseRow{}, NamedWords: "Its name in cuts is “" + r.Name + "”."}
	if v != nil {
		c.FileName = v.Source.Name
		line := fmt.Sprintf("Added %s", v.CreatedAt.Local().Format("2 Jan 2006, 3:04 pm"))
		switch v.Source.Kind {
		case models.FromURL:
			line += " from " + firstNonEmpty(v.Source.Site, v.Source.URL)
			c.Link = v.Source.URL
		case models.FromFolder:
			line += " from a linked folder"
		case models.FromGenerated:
			line += ", made by MetaRush"
			if len(v.Source.Lineage) > 0 && v.Source.Lineage[0].Step != "" {
				line += " (" + v.Source.Lineage[0].Step + ")"
			}
		}
		c.Origin = append(c.Origin, line+".")
		if v.Width > 0 {
			c.Found = append(c.Found, fmt.Sprintf("%d × %d pixels%s.", v.Width, v.Height, map[bool]string{true: ", with a see-through background", false: ""}[v.HasAlpha]))
		}
		if len(v.Colors) > 0 {
			c.Found = append(c.Found, "Main colours: "+strings.Join(v.Colors[:min(4, len(v.Colors))], ", ")+".")
		}
		if t := strings.TrimSpace(v.Text); t != "" {
			c.OnScreen = []string{clipText(t, 80)}
			c.Found = append(c.Found, "Words in it: “"+clipText(t, 80)+"”.")
		}
	}
	if r.License != "" {
		c.Origin = append(c.Origin, "Licence: "+r.License+".")
	}
	if len(r.Versions) > 1 {
		c.Found = append(c.Found, fmt.Sprintf("%d versions.", len(r.Versions)))
	}
	if r.Kind == models.ResourceImage {
		fv, err := pictureView(ctx, a, r.ID)
		if err == nil {
			c.Fixes = fv
			for _, cu := range fv.Cuts {
				c.Uses = append(c.Uses, ops.UseRow{Kind: "cut", ProjectID: cu.ProjectID, ProjectName: cu.ProjectName, Branch: cu.Branch, RevisionID: cu.RevisionID,
					Text: fmt.Sprintf("In “%s” of %s, version %d", cu.CutName, cu.ProjectName, cu.Number), Link: fmt.Sprintf("/p/%s/r/%s", cu.ProjectID, cu.RevisionID)})
			}
		}
	}
	c.Summary = fmt.Sprintf("“%s”: a %s", c.Title, models.SourceKindWords(models.SourcePicture))
	return c, nil
}

func recorded(as *models.Asset) (time.Time, bool) {
	for _, f := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, as.Media.CreationTime); err == nil && t.Year() > 1990 {
			return t, true
		}
	}
	return time.Time{}, false
}

func articled(s string) string {
	if s == "" {
		return s
	}
	switch strings.ToLower(s[:1]) {
	case "a", "e", "i", "o", "u":
		return "an " + s
	}
	return "a " + s
}

func viaWords(via string) string {
	switch via {
	case "dashboard":
		return "in the dashboard"
	case "cli":
		return "from a terminal"
	case "watch":
		return "from a watched folder"
	case models.ViaExtension:
		return "from the browser extension"
	case "worker":
		return "by MetaRush"
	case models.ViaAssistant, models.ViaAssistantAuto:
		return "through Claude"
	}
	return ""
}

// lengthLine: "A camera video, 2 min 05 s, 1920 × 1080 at 30 frames a second, HDR."
func lengthLine(as *models.Asset, kind string) string {
	parts := []string{"A " + models.SourceKindWords(kind), clockWords(as.Media.Duration)}
	if as.Media.HasVideo && as.Media.Width > 0 {
		parts = append(parts, fmt.Sprintf("%d × %d at %s frames a second", as.Media.Width, as.Media.Height, trimFloat(as.Media.FPS)))
	}
	if as.Media.HDR {
		parts = append(parts, "HDR")
	}
	if as.Media.VFR {
		parts = append(parts, "its frame rate changes while it plays")
	}
	return strings.Join(parts, ", ") + "."
}

func clockWords(sec float64) string {
	s := int(math.Round(sec))
	switch {
	case s < 60:
		return fmt.Sprintf("%d seconds", s)
	case s < 3600:
		return fmt.Sprintf("%d min %02d s", s/60, s%60)
	}
	return fmt.Sprintf("%d h %02d min", s/3600, (s%3600)/60)
}

func trimFloat(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.01 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// soundLine: the channels and how loud it is.
func soundLine(ctx context.Context, a *app.App, as *models.Asset) string {
	ch := 0
	for _, s := range as.Media.Streams {
		if s.Type == "audio" {
			ch = s.Channels
			break
		}
	}
	line := map[int]string{1: "Mono sound", 2: "Stereo sound"}[ch]
	if line == "" {
		line = fmt.Sprintf("Sound with %d channels", ch)
	}
	if l := evidence.LoadLoudness(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]; l.IntegratedLUFS != nil {
		switch v := *l.IntegratedLUFS; {
		case v < -30:
			line += ", very quiet"
		case v < -20:
			line += ", on the quiet side"
		case v > -10:
			line += ", very loud"
		default:
			line += ", at an ordinary loudness"
		}
	}
	return line + "."
}

// peopleLine: the voices and faces, named or not.
func peopleLine(ctx context.Context, a *app.App, as *models.Asset) string {
	var voices []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": as.ID, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &voices)
	named, unnamed := []string{}, 0
	for _, v := range voices {
		if v.Minor {
			continue
		}
		if v.Name != "" {
			named = append(named, v.Name)
		} else {
			unnamed++
		}
	}
	faces, _ := a.DB.C(store.CFaces).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID})
	var parts []string
	if len(named) > 0 {
		parts = append(parts, "Voices: "+joinAnd(named))
	}
	if unnamed > 0 {
		parts = append(parts, fmt.Sprintf("%d voice%s nobody named yet", unnamed, map[bool]string{true: "", false: "s"}[unnamed == 1]))
	}
	if faces > 0 {
		parts = append(parts, fmt.Sprintf("%d face%s seen", faces, map[bool]string{true: "", false: "s"}[faces == 1]))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

func sourcesPeople(ctx context.Context, a *app.App, assetID string) []string {
	var voices []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": assetID, "name": bson.M{"$nin": bson.A{nil, ""}}}, &voices)
	var faces []models.FaceTrack
	_ = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": assetID, "name": bson.M{"$nin": bson.A{nil, ""}}}, &faces)
	seen := map[string]bool{}
	var out []string
	for _, v := range voices {
		if !seen[v.Name] {
			seen[v.Name] = true
			out = append(out, v.Name)
		}
	}
	for _, f := range faces {
		if !seen[f.Name] {
			seen[f.Name] = true
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// screenWords are the longest-showing words on screen (up to three).
func screenWords(idx *visual.Index) []string {
	type tw struct {
		text string
		dur  float64
	}
	byText := map[string]float64{}
	for _, t := range idx.Text {
		s := strings.Join(strings.Fields(t.Text), " ")
		if t.Changing || len([]rune(s)) < 3 || t.Conf < 0.5 {
			continue
		}
		byText[s] += t.To - t.From
	}
	var list []tw
	for s, d := range byText {
		list = append(list, tw{s, d})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].dur != list[j].dur {
			return list[i].dur > list[j].dur
		}
		return list[i].text < list[j].text
	})
	var out []string
	for i := 0; i < len(list) && i < 3; i++ {
		out = append(out, clipText(list[i].text, 60))
	}
	return out
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
