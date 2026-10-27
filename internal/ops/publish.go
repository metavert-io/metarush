package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Publishing to video platforms (D305; docs/publishing.md): a people-only
// step every time — never Claude, never Auto, never an agent — through the
// person's own sign-in on this computer. publish.plan says exactly what would
// be sent (the final render, the title, description and chapters from the
// delivery notes, the captions files, the privacy, who it is posted as) and
// what holds it; publish.send sends that, records it on the version and the
// receipt. Nothing is sent while anything holds it.

// A post goes out like a delivery: the ship gate (D298–D302) holds it while
// the quality check or a sign-off holds the version (unless a person sent it
// out anyway, D299), and What may leave (client rules, D288–D290, place
// delivery, receiver the platform) keeps a client's picture, sound, faces and
// words from a platform their rules don't allow. A plan says so quietly; a
// post that is refused is recorded.

// LeaveQuiet marks a context whose client-rules refusals are only said (a
// plan), set by the client rules' package.
var LeaveQuiet func(ctx context.Context) context.Context

// PublishPlatforms names the platforms in words.
var PublishPlatforms = map[string]string{publish.YouTube: "YouTube"}

// PublishIn names a version and how to post it.
type PublishIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or exact name)."`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty"`
	Platform  string `json:"platform,omitempty" jsonschema:"enum=,enum=youtube,description=Where it is posted (default youtube)."`
	Privacy   string `json:"privacy,omitempty" jsonschema:"enum=,enum=private,enum=unlisted,enum=public,description=Who can see it (default private: you make it public on the platform when you're ready)."`
	Preset    string `json:"preset,omitempty" jsonschema:"description=Which shape's final render (default: the landscape one\\, else the only one)."`
	// Fingerprint is the plan a person saw (publish.send sends only that).
	Fingerprint string `json:"fingerprint,omitempty"`
	// Attempt: a person asked to post it (the terminal's post): a refusal by
	// the client's rules is recorded, as a post's is.
	Attempt bool `json:"attempt,omitempty" jsonschema:"description=A person asked to post it: a refusal by the client's rules is recorded."`
	// Again posts it although this render is already on the same channel (a
	// second video there); a person is asked first.
	Again bool `json:"again,omitempty" jsonschema:"description=Post it again although this render was already posted to the same channel (a second video there)."`
}

// PublishHold is something that keeps a version from being posted.
type PublishHold struct {
	Says    string `json:"says"`
	Command string `json:"command,omitempty"`
	// People: only a person can clear it (signing in, approving, setting up).
	People bool `json:"people,omitempty"`
	// Sending: it is being posted right now (the hold clears by itself).
	Sending bool `json:"sending,omitempty"`
	// Code is the error a post refused for it carries (E_HELD: the ship gate;
	// E_KEPT_HERE: the client's rules).
	Code string `json:"code,omitempty"`
}

// PublishCaption is a captions file that goes with the video.
type PublishCaption struct {
	Language          string `json:"language"`
	Name              string `json:"name"`
	Lines             int    `json:"lines"`
	MachineTranslated bool   `json:"machineTranslated,omitempty"`
}

// PublishRender is the file that would be sent.
type PublishRender struct {
	ID      string  `json:"id"`
	Preset  string  `json:"preset"`
	Seconds float64 `json:"seconds"`
	Size    int64   `json:"size"`
	SHA256  string  `json:"sha256"`
}

// PublishPlanOut is exactly what would be posted, and what holds it.
type PublishPlanOut struct {
	Platform     string               `json:"platform"`
	PlatformName string               `json:"platformName"`
	ProjectID    string               `json:"projectId"`
	ProjectName  string               `json:"projectName"`
	RevisionID   string               `json:"revisionId"`
	Number       int                  `json:"number"`
	Ready        bool                 `json:"ready" jsonschema:"description=Nothing holds it: a person can post it."`
	Holds        []PublishHold        `json:"holds"`
	As           *publish.Identity    `json:"as,omitempty" jsonschema:"description=Who it is posted as (the asker's own sign-in on this computer)."`
	Render       *PublishRender       `json:"render,omitempty"`
	Title        string               `json:"title"`
	Description  string               `json:"description" jsonschema:"description=The description as posted: the delivery notes' description\\, the chapters\\, disclosures and credits."`
	Chapters     []string             `json:"chapters"`
	Captions     []PublishCaption     `json:"captions"`
	Privacy      string               `json:"privacy"`
	Synthetic    bool                 `json:"synthetic" jsonschema:"description=The platform is told the video contains AI-made content."`
	Notes        []string             `json:"notes" jsonschema:"description=Plain warnings: what the platform may do differently\\, what is missing."`
	Earlier      []models.Publication `json:"earlier" jsonschema:"description=Earlier posts of this version."`
	// Sending is the post going out now, with how much has gone.
	Sending *models.Publication `json:"sending,omitempty"`
	// Posted is this render already on the same channel: posting again makes
	// a second video there, so a person is asked (again).
	Posted      *models.Publication `json:"posted,omitempty"`
	Fingerprint string              `json:"fingerprint"`
	Summary     string              `json:"summary"`
	// sentAnyway are the notes that let it out past the ship gate (D299).
	sentAnyway []models.ShipOverride
}

// PublishSendOut is the posted video.
type PublishSendOut struct {
	Plan        PublishPlanOut     `json:"plan"`
	Publication models.Publication `json:"publication"`
	Summary     string             `json:"summary"`
}

// PublishPlatformsIn asks about the platforms.
type PublishPlatformsIn struct{}

// PublishPlatform is one platform and the asker's sign-in on this computer.
type PublishPlatform struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	// SetUp: an owner registered the platform client on this computer.
	SetUp bool              `json:"setUp"`
	As    *publish.Identity `json:"as,omitempty" jsonschema:"description=Your sign-in on this computer (none: not signed in)."`
	// Next is what a person does next, in plain words; Command runs it.
	Next    string `json:"next"`
	Command string `json:"command,omitempty"`
}

// PublishPlatformsOut lists them.
type PublishPlatformsOut struct {
	Platforms []PublishPlatform `json:"platforms"`
}

// PublishSetupIn registers a platform client on this computer (an owner).
type PublishSetupIn struct {
	Platform     string `json:"platform" jsonschema:"enum=youtube"`
	ClientID     string `json:"clientId,omitempty" jsonschema:"description=The OAuth client ID (a desktop app) from your own Google Cloud project."`
	ClientSecret string `json:"clientSecret,omitempty" jsonschema:"description=Its secret: typed at the terminal\\, never a command-line argument."`
	Remove       bool   `json:"remove,omitempty" jsonschema:"description=Forget the client on this computer (everyone's sign-ins stop working)."`
}

// PublishConnectIn signs the asker in on this computer.
type PublishConnectIn struct {
	Platform string `json:"platform" jsonschema:"enum=youtube"`
	// Forget takes the sign-in back (at the platform too) instead.
	Forget bool `json:"forget,omitempty"`
}

// PublishConnectOut is the sign-in.
type PublishConnectOut struct {
	Platform string            `json:"platform"`
	As       *publish.Identity `json:"as,omitempty"`
	Summary  string            `json:"summary"`
}

func publishPerson(a *app.App) string {
	if a.Actor.UserID != "" {
		return a.Actor.UserID
	}
	return "owner"
}

func platformOf(p string) (string, string, error) {
	if p == "" {
		p = publish.YouTube
	}
	name, ok := PublishPlatforms[p]
	if !ok {
		return "", "", invalid("MetaRush posts to YouTube; %q isn't a platform it knows.", p)
	}
	return p, name, nil
}

// notByAgents refuses publishing for anything but a person (on top of
// HumanOnly): no Auto, no AI app, no assistant.
func notByAgents(a *app.App) error {
	switch {
	case a.Actor.Via == models.ViaAssistant || a.Actor.Via == "assistant-auto" || a.Actor.Agent != "" || a.ProposeOnly:
		return apperr.New(apperr.HumanOnly, "Only a person posts videos, every time: Claude, Auto and AI apps never do.").
			WithDetails(map[string]any{"reason": "people_only"})
	}
	return nil
}

// finalRender is a version's newest final render of a shape (any shape when
// preset is "": landscape first).
func finalRender(ctx context.Context, a *app.App, rev *models.Revision, preset string) (*models.Render, []string, error) {
	var renders []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "state": models.RenderAvailable, "quality": "final"}, &renders,
		options.Find().SetSort(bson.D{{Key: "completedAt", Value: -1}})); err != nil {
		return nil, nil, err
	}
	byPreset := map[string]*models.Render{}
	var shapes []string
	for i := range renders {
		r := &renders[i]
		if r.Output == nil || r.SpecHash != rev.SpecHash {
			continue
		}
		if byPreset[r.Preset] == nil {
			byPreset[r.Preset] = r
			shapes = append(shapes, r.Preset)
		}
	}
	sort.Strings(shapes)
	if preset != "" {
		return byPreset[preset], shapes, nil
	}
	if r := byPreset[editspec.PresetLandscape]; r != nil {
		return r, shapes, nil
	}
	if len(shapes) > 0 {
		return byPreset[shapes[0]], shapes, nil
	}
	return nil, shapes, nil
}

// versionLanguage is the language most of a version's clips are spoken in.
func versionLanguage(a *app.App, assets map[string]models.Asset) string {
	count := map[string]int{}
	best, most := "", 0
	for _, as := range assets {
		code := ""
		switch {
		case as.Language != nil && as.Language.Code != "":
			code = as.Language.Code
		case as.Transcript != nil && as.Transcript.Language != "":
			code = as.Transcript.Language
		}
		if code == "" {
			continue
		}
		count[code]++
		if count[code] > most || (count[code] == most && code < best) {
			best, most = code, count[code]
		}
	}
	if best == "" {
		best = firstNonEmpty(a.Cfg.Transcription.Language, "en")
	}
	return best
}

// publishPlan works out exactly what would be posted.
func publishPlan(ctx context.Context, a *app.App, in *PublishIn, sending bool) (*PublishPlanOut, *publish.Video, *models.Render, error) {
	platform, name, err := platformOf(in.Platform)
	if err != nil {
		return nil, nil, nil, err
	}
	privacy := strings.ToLower(strings.TrimSpace(in.Privacy))
	switch privacy {
	case "":
		privacy = publish.Private
	case publish.Private, publish.Unlisted, publish.Public:
	default:
		return nil, nil, nil, invalid("Privacy is private, unlisted or public, not %q.", in.Privacy)
	}
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	rev, err := ResolveRevision(ctx, a, p, in.Revision, in.Branch)
	if err != nil {
		return nil, nil, nil, err
	}
	out := &PublishPlanOut{Platform: platform, PlatformName: name, ProjectID: p.ID, ProjectName: p.Name, RevisionID: rev.ID, Number: rev.Number, Privacy: privacy,
		Holds: []PublishHold{}, Chapters: []string{}, Captions: []PublishCaption{}, Notes: []string{}, Earlier: []models.Publication{}}
	for _, x := range rev.Publications {
		if x.Platform == platform {
			out.Earlier = append(out.Earlier, x)
		}
	}
	version := fmt.Sprintf("Version %d", rev.Number)
	hold := func(h PublishHold) { out.Holds = append(out.Holds, h) }

	// Who it is posted as.
	_, setUpErr := publish.LoadClient(a.Cfg, platform)
	switch {
	case setUpErr != nil:
		hold(PublishHold{Says: name + " isn't set up on this computer yet: an owner registers MetaRush with their own Google Cloud project once.", Command: "metarush publish setup " + platform, People: true})
	default:
		out.As = publish.SignedIn(a.Cfg, platform, publishPerson(a))
		if out.As == nil {
			hold(PublishHold{Says: "You aren't signed in to " + name + " on this computer.", Command: "metarush publish connect " + platform, People: true})
		}
	}
	// The approval: only approved versions go out.
	switch rev.Approval.State {
	case models.ApprovalApproved:
	case models.ApprovalRejected:
		hold(PublishHold{Says: version + " needs changes, so it never goes out: make a new version."})
	default:
		hold(PublishHold{Says: version + " isn't approved yet.", Command: "metarush revision approve " + rev.ID, People: true})
	}
	// One at a time: a post going out holds the next one.
	for i := range out.Earlier {
		if x := &out.Earlier[i]; x.State == models.PublicationSending {
			out.Sending = x
			hold(PublishHold{Says: fmt.Sprintf("%s is being posted to %s as “%s” now (%s).", version, name, x.Channel, sentWords(x)), Sending: true})
		}
	}
	// The final render.
	render, shapes, err := finalRender(ctx, a, rev, in.Preset)
	if err != nil {
		return nil, nil, nil, err
	}
	if render == nil {
		what := "a final render"
		if in.Preset != "" {
			what = "a final render in the shape " + editspec.PresetLabel(in.Preset)
			if len(shapes) > 0 {
				what += " (it has " + andList(presetLabels(shapes)) + ")"
			}
		}
		hold(PublishHold{Says: version + " has no " + strings.TrimPrefix(what, "a ") + " yet.", Command: "metarush render submit --revision " + rev.ID + " --quality final --wait"})
	} else {
		out.Render = &PublishRender{ID: render.ID, Preset: render.Preset, Seconds: render.Output.Duration, Size: render.Output.Size, SHA256: render.Output.SHA256}
		if len(shapes) > 1 && in.Preset == "" {
			out.Notes = append(out.Notes, fmt.Sprintf("It has final renders in %s; the %s one is sent (choose another with its shape).", andList(presetLabels(shapes)), editspec.PresetLabel(render.Preset)))
		}
	}
	// The ship gate: what holds the final render back from going out (the
	// approval and the render are held above), unless a person sent it out anyway.
	var presets []string
	if r := out.renderPreset(); r != "" {
		presets = []string{r}
	}
	if gate, err := ShipGate(ctx, a, p, rev, "final", presets); err != nil {
		return nil, nil, nil, err
	} else if !gate.letsOut() {
		for _, h := range gate.Holds {
			switch {
			case h.Kind == models.HoldConsent: // changed words whose consent no longer holds: never posted (D309)
				hold(PublishHold{Says: h.Says, Command: fmt.Sprintf("metarush deliver ready %s --version r%d", p.ID, rev.Number), People: true, Code: apperr.ConsentRequired})
			case h.Overridable && !h.Covered:
				hold(PublishHold{Says: h.Says, Command: fmt.Sprintf("metarush deliver ready %s --version r%d", p.ID, rev.Number), People: true, Code: apperr.Held})
			}
		}
	} else if rec := gate.record(); len(rec.SentAnyway) > 0 {
		out.sentAnyway = rec.SentAnyway
		out.Notes = append(out.Notes, fmt.Sprintf("%s goes out past what held it, with the note a person kept (it shows on the receipt).", version))
	}
	// What may leave: a client's things go to the platform only where their rules allow.
	lctx := ctx
	if !sending && !(in.Attempt && CallerFrom(ctx).Human) && LeaveQuiet != nil {
		lctx = LeaveQuiet(ctx)
	}
	// Every project the version touches (its clips can be in other clients'
	// projects too), as delivery asks.
	if err := MayLeave(lctx, a, models.PlaceDelivery, platform, name, deliveryKinds, revisionTouches(ctx, a, rev), fmt.Sprintf("version %d of “%s”", rev.Number, p.Name)); err != nil {
		e := apperr.From(err)
		hold(PublishHold{Says: e.Message, Command: e.Command(), People: true, Code: e.Code})
	}

	// What it says: the delivery notes (D237).
	notes := rev.DeliveryNotes
	cut := firstNonEmpty(rev.Spec.Title, p.Name)
	if notes == nil || notes.Title == "" {
		out.Title = cut
		out.Notes = append(out.Notes, "The delivery notes have no title, so the cut's name is the title. Write delivery notes first to choose one.")
	} else {
		out.Title = notes.Title
	}
	if r := []rune(out.Title); len(r) > 100 {
		out.Title = string(r[:99]) + "…"
		out.Notes = append(out.Notes, "The title is longer than 100 characters, YouTube's most: it is cut short.")
	}
	var desc []string
	if notes != nil && strings.TrimSpace(notes.Description) != "" {
		desc = append(desc, strings.TrimSpace(notes.Description))
	}
	if notes != nil && len(notes.Chapters) > 0 {
		out.Chapters = ChapterLines(notes.Chapters)
		if len(notes.Chapters) < 3 {
			out.Notes = append(out.Notes, "YouTube shows chapters only when there are at least three; the chapter lines are in the description anyway.")
		}
		desc = append(desc, strings.Join(out.Chapters, "\n"))
	}
	// Disclosures and credits, as delivery writes them.
	var assets map[string]models.Asset
	if render != nil {
		if assets, err = deliveryAssets(ctx, a, &rev.Spec); err != nil {
			return nil, nil, nil, err
		}
		extras, _ := LoadRenderExtras(ctx, a, &rev.Spec)
		sc := buildSidecar(a, &deliveryItem{rev: *rev, proj: *p, render: *render, assets: assets, takes: deliveryTakes(ctx, a, &rev.Spec), extras: extras}, "video.mp4")
		var tail []string
		if sc.Generated != nil && sc.Generated.Notice != "" {
			tail = append(tail, sc.Generated.Notice)
			out.Synthetic = true
		}
		if sc.Narration != nil && sc.Narration.AIVoice && sc.Narration.Notice != "" {
			tail = append(tail, sc.Narration.Notice)
			out.Synthetic = true
		}
		for _, c := range sc.Credits {
			tail = append(tail, c.Credit)
		}
		if len(tail) > 0 {
			desc = append(desc, strings.Join(tail, "\n"))
		}
		if out.Synthetic {
			out.Notes = append(out.Notes, name+" is told the video contains AI-made content (its altered or synthetic content setting).")
		}
	}
	out.Description = strings.Join(desc, "\n\n")
	if r := []rune(out.Description); len(r) > 5000 {
		out.Description = string(r[:4999]) + "…"
		out.Notes = append(out.Notes, "The description is longer than 5,000 characters, YouTube's most: it is cut short.")
	}
	if strings.ContainsAny(out.Title+out.Description, "<>") {
		out.Notes = append(out.Notes, "YouTube doesn't take < or > in titles and descriptions: they are left out.")
		out.Title = strings.NewReplacer("<", "", ">", "").Replace(out.Title)
		out.Description = strings.NewReplacer("<", "", ">", "").Replace(out.Description)
	}

	// Captions files (D148, D238), where the place takes them.
	video := &publish.Video{Title: out.Title, Description: out.Description, Privacy: privacy, Synthetic: out.Synthetic, Language: versionLanguage(a, assets)}
	if render != nil && transcripts.CaptionFileAllowed(rev.Spec.Output.Platform) {
		langs, _ := projectLanguages(ctx, a, p.ID)
		for _, lang := range append([]string{""}, langs...) {
			cf, err := CallTyped[CaptionsFileOut](ctx, a, "captions.file", CaptionsFileIn{ProjectID: p.ID, Revision: rev.ID, Format: "vtt", Language: lang}, Caller{})
			if err != nil || cf.Lines == 0 {
				continue
			}
			code := firstNonEmpty(cf.Language, video.Language)
			label := firstNonEmpty(cf.LanguageName, languageName(code))
			if cf.MachineTranslated {
				label += " (machine-translated)"
			}
			out.Captions = append(out.Captions, PublishCaption{Language: code, Name: label, Lines: cf.Lines, MachineTranslated: cf.MachineTranslated})
			video.Captions = append(video.Captions, publish.Caption{Language: code, Name: label, Format: "vtt", Content: cf.Content})
		}
	}
	if privacy != publish.Private {
		out.Notes = append(out.Notes, "If YouTube hasn't reviewed the client set up on this computer, it keeps every video sent through it private until you change it in YouTube Studio.")
	}
	if render != nil && out.As != nil {
		for i := range out.Earlier {
			if x := &out.Earlier[i]; x.State == models.PublicationPosted && x.RenderID == render.ID && x.ChannelID == out.As.ChannelID {
				out.Posted = x
			}
		}
		if x := out.Posted; x != nil {
			out.Notes = append(out.Notes, fmt.Sprintf("%s is already on %s as “%s” (%s). Posting it again makes a second video there.", version, name, x.Channel, firstNonEmpty(x.URL, x.At.Format("2 Jan 2006"))))
		}
	}
	out.Ready = len(out.Holds) == 0
	out.Fingerprint = publishFingerprint(out)
	who := ""
	if out.As != nil {
		who = " as “" + out.As.Channel + "”"
	}
	if out.Ready {
		out.Summary = fmt.Sprintf("%s of “%s” is ready to post to %s%s, %s: “%s”, %s, %d captions file(s).", version, p.Name, name, who, privacy, out.Title, clockLen(out.Render.Seconds), len(out.Captions))
	} else {
		out.Summary = fmt.Sprintf("%s of “%s” can't be posted to %s yet. %s", version, p.Name, name, out.Holds[0].Says)
	}
	return out, video, render, nil
}

func (p *PublishPlanOut) renderPreset() string {
	if p.Render != nil {
		return p.Render.Preset
	}
	return ""
}

func presetLabels(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, editspec.PresetLabel(p))
	}
	return out
}

func languageName(code string) string {
	if n, ok := transcripts.Language(code); ok {
		return n
	}
	return code
}

func clockLen(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// publishFingerprint is the plan a person saw: the file, the words, the
// captions, the privacy and who it is posted as.
func publishFingerprint(p *PublishPlanOut) string {
	h := sha256.New()
	b, _ := json.Marshal(struct {
		R       *PublishRender
		T, D, P string
		C       []PublishCaption
		A       *publish.Identity
		S       bool
	}{p.Render, p.Title, p.Description, p.Privacy, p.Captions, p.As, p.Synthetic})
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func init() {
	Def("publish.platforms", "The video platforms MetaRush posts to (YouTube) and your own sign-in to each on this computer, with what a person does next", Flags{},
		func(ctx context.Context, a *app.App, in *PublishPlatformsIn) (*PublishPlatformsOut, error) {
			out := &PublishPlatformsOut{Platforms: []PublishPlatform{}}
			for _, id := range []string{publish.YouTube} {
				pp := PublishPlatform{Platform: id, Name: PublishPlatforms[id]}
				_, err := publish.LoadClient(a.Cfg, id)
				pp.SetUp = err == nil
				if pp.SetUp {
					pp.As = publish.SignedIn(a.Cfg, id, publishPerson(a))
				}
				switch {
				case !pp.SetUp:
					pp.Next = "An owner registers MetaRush with their own Google Cloud project once, at this computer; then each person signs in with their own channel there."
					pp.Command = "metarush publish setup " + id
				case pp.As == nil:
					pp.Next = "Sign in with your own channel, at this computer."
					pp.Command = "metarush publish connect " + id
				default:
					pp.Next = "Post an approved version from its page."
					pp.Command = "metarush publish post <project> --version rN"
				}
				out.Platforms = append(out.Platforms, pp)
			}
			return out, nil
		})
	Def("publish.setup", "Register MetaRush with a video platform on this computer with the owner's own client (YouTube: an OAuth client for a desktop app from their Google Cloud project), kept in the keychain; or forget it", Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *PublishSetupIn) (*PublishConnectOut, error) {
			platform, name, err := platformOf(in.Platform)
			if err != nil {
				return nil, err
			}
			if err := notByAgents(a); err != nil {
				return nil, err
			}
			if in.Remove {
				if err := publish.RemoveClient(a.Cfg, platform); err != nil {
					return nil, apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't remove the %s client from this computer's keychain.", name)
				}
				a.Audit(ctx, "publish.setup", "platform", platform, "", map[string]any{"removed": true})
				return &PublishConnectOut{Platform: platform, Summary: name + " is no longer set up on this computer: nobody can post to it from here until an owner sets it up again."}, nil
			}
			if err := publish.SaveClient(a.Cfg, platform, publish.Client{ID: in.ClientID, Secret: in.ClientSecret}); err != nil {
				var p *publish.Problem
				if errors.As(err, &p) {
					return nil, invalid("%s", p.Msg)
				}
				return nil, apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't keep the %s client in this computer's keychain.", name)
			}
			a.Audit(ctx, "publish.setup", "platform", platform, "", map[string]any{"clientId": in.ClientID})
			return &PublishConnectOut{Platform: platform, Summary: name + " is set up on this computer. Each person now signs in with their own channel, at this computer."}, nil
		})
	Def("publish.connect", "Sign in to a video platform with your own account, in your browser on this computer (people only); the lasting permission is kept in this computer's keychain under you. forget takes it back", Flags{Mutating: true, HumanOnly: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *PublishConnectIn) (*PublishConnectOut, error) {
			platform, name, err := platformOf(in.Platform)
			if err != nil {
				return nil, err
			}
			if err := notByAgents(a); err != nil {
				return nil, err
			}
			person := publishPerson(a)
			if in.Forget {
				acct, err := publish.ForgetYouTube(ctx, a.Cfg, person)
				if errors.Is(err, publish.ErrNotSignedIn) {
					return &PublishConnectOut{Platform: platform, Summary: "You weren't signed in to " + name + " on this computer."}, nil
				}
				if err != nil {
					return nil, publishErr(err, name)
				}
				a.Audit(ctx, "publish.connect", "platform", platform, "", map[string]any{"forgot": true, "channel": acct.Channel})
				return &PublishConnectOut{Platform: platform, Summary: "Signed out of " + name + " (“" + acct.Channel + "”) on this computer, and the permission was taken back at Google."}, nil
			}
			acct, err := publish.ConnectYouTube(ctx, a.Cfg, person, publishOpener(ctx, a))
			if err != nil {
				return nil, publishErr(err, name)
			}
			a.Audit(ctx, "publish.connect", "platform", platform, "", map[string]any{"channel": acct.Channel, "channelId": acct.ChannelID})
			return &PublishConnectOut{Platform: platform, As: acct, Summary: "Signed in to " + name + " as “" + acct.Channel + "” on this computer. Approved versions can be posted from their page."}, nil
		})
	Def("publish.plan", "What posting a version to a video platform would send — the final render, the title, description and chapters from its delivery notes, captions files, privacy, who it is posted as — and what holds it (approval, a final render, the ship gate, client rules, signing in); nothing is sent",
		Flags{}, func(ctx context.Context, a *app.App, in *PublishIn) (*PublishPlanOut, error) {
			out, _, _, err := publishPlan(ctx, a, in, false)
			return out, err
		})
	Def("publish.send", "Post an approved version to a video platform as you (people only, every time; never Claude, Auto or an AI app): exactly what publish.plan shows, recorded on the version and the receipt", Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PublishIn) (*PublishSendOut, error) {
			if err := notByAgents(a); err != nil {
				return nil, err
			}
			plan, video, render, err := publishPlan(ctx, a, in, true)
			if err != nil {
				return nil, err
			}
			if !plan.Ready {
				return nil, PublishRefusal(plan)
			}
			if in.Fingerprint != "" && in.Fingerprint != plan.Fingerprint {
				return nil, apperr.New(apperr.Conflict, "What would be posted changed since you looked (the render, the delivery notes, the captions or the sign-in). Look again before posting.").
					WithDetails(map[string]any{"fingerprint": plan.Fingerprint})
			}
			if plan.Posted != nil && !in.Again {
				return nil, apperr.New(apperr.Conflict, "Version %d is already on %s as “%s”. Posting it again makes a second video there: say so to post it again.", plan.Number, plan.PlatformName, plan.Posted.Channel).
					WithDetails(map[string]any{"publication": plan.Posted.ID, "url": plan.Posted.URL, "reason": "already_posted"}).
					WithCommand(fmt.Sprintf("metarush publish post %s --version r%d --again", plan.ProjectID, plan.Number))
			}
			src, err := a.Cache.Ensure(ctx, a.Media, render.Output.StorageKey, render.Output.SHA256, render.Output.Size, nil)
			if err != nil {
				return nil, apperr.Wrap(apperr.Storage, err, "The final render couldn't be brought to this computer, so nothing was posted.").WithHint("Check the internet connection and try again.")
			}
			video.Path, video.Size = src, render.Output.Size
			now := time.Now().UTC()
			pub := models.Publication{ID: ids.New("pub"), Platform: plan.Platform, State: models.PublicationSending, Channel: plan.As.Channel, ChannelID: plan.As.ChannelID,
				Privacy: plan.Privacy, Title: plan.Title, RenderID: render.ID, SHA256: render.Output.SHA256, Preset: render.Preset, Chapters: len(plan.Chapters), Disclosed: plan.Synthetic,
				By: a.Actor, At: now, Size: render.Output.Size, Installation: a.Cfg.Installation.ID, Beat: &now, Claim: ids.New("clm")}
			for _, o := range plan.sentAnyway {
				pub.Overrides = append(pub.Overrides, o.ID) // the notes that let it out past the ship gate
			}
			// Only one post of a render to a channel at a time, and none again
			// unless a person said so: the check and the new entry are one write
			// (two tabs or a double click can't both send).
			states := []string{models.PublicationSending, models.PublicationPosted}
			if in.Again {
				states = []string{models.PublicationSending}
			}
			revs := a.DB.C(store.CRevisions)
			res, err := revs.UpdateOne(ctx, bson.M{"_id": plan.RevisionID, "workspaceId": a.WorkspaceID(),
				"publications": bson.M{"$not": bson.M{"$elemMatch": bson.M{"platform": plan.Platform, "channelId": plan.As.ChannelID, "renderId": render.ID, "state": bson.M{"$in": states}}}}},
				bson.M{"$push": bson.M{"publications": pub}})
			if err != nil {
				return nil, err
			}
			if res.MatchedCount == 0 {
				return nil, alreadyPosting(ctx, a, plan, render.ID)
			}
			// A dashboard tab closed mid-way doesn't stop the upload; a terminal's Ctrl-C does.
			sendCtx := ctx
			if a.Actor.Via == "dashboard" {
				sendCtx = context.WithoutCancel(ctx)
			}
			j := &publishJournal{Publication: pub.ID, Revision: plan.RevisionID, Project: plan.ProjectID, Number: plan.Number, Person: publishPerson(a),
				RenderKey: render.Output.StorageKey, SHA256: render.Output.SHA256, Size: render.Output.Size, Video: *video}
			posted, sendErr := sendPublication(sendCtx, a, j, pub, a.Actor.Via != "dashboard")
			if sendErr != nil {
				return nil, publishErr(sendErr, plan.PlatformName)
			}
			// What the version says now (another MetaRush may have finished it).
			if rec := publicationOf(context.WithoutCancel(ctx), a, plan.RevisionID, pub.ID); rec != nil {
				pub = *rec
			}
			if posted.Late {
				return &PublishSendOut{Plan: *plan, Publication: pub, Summary: posted.Notes[len(posted.Notes)-1]}, nil
			}
			out := &PublishSendOut{Plan: *plan, Publication: pub}

			out.Summary = fmt.Sprintf("Posted version %d of “%s” to %s as “%s” (%s): %s", plan.Number, plan.ProjectName, plan.PlatformName, plan.As.Channel, plan.Privacy, posted.URL)
			return out, nil
		})
}

// PublishRefusal is why a plan can't be posted, as the error a post gets: the
// client's rules and the ship gate refuse with their own codes (E_KEPT_HERE,
// E_HELD; skills branch on them), anything else says what to do first.
func PublishRefusal(plan *PublishPlanOut) error {
	code, cmd := apperr.InvalidInput, "metarush publish plan"
	if len(plan.Holds) > 0 {
		cmd = firstNonEmpty(plan.Holds[0].Command, cmd)
	}
	for _, want := range []string{apperr.KeptHere, apperr.Held} {
		for _, h := range plan.Holds {
			if h.Code == want && code == apperr.InvalidInput {
				code, cmd = want, firstNonEmpty(h.Command, cmd)
			}
		}
	}
	return apperr.New(code, "%s", plan.Summary).WithDetails(map[string]any{"holds": plan.Holds}).WithCommand(cmd)
}

// alreadyPosting says why a post wasn't started: one is going out now, or
// this render is already on the channel.
func alreadyPosting(ctx context.Context, a *app.App, plan *PublishPlanOut, renderID string) error {
	var rev models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), plan.RevisionID, &rev)
	for _, x := range rev.Publications {
		if x.Platform == plan.Platform && x.ChannelID == plan.As.ChannelID && x.RenderID == renderID && x.State == models.PublicationSending {
			return apperr.New(apperr.Conflict, "Version %d is being posted to %s as “%s” now (%s): it isn't sent twice.", plan.Number, plan.PlatformName, x.Channel, sentWords(&x)).
				WithDetails(map[string]any{"publication": x.ID, "reason": "sending"})
		}
	}
	return apperr.New(apperr.Conflict, "Version %d is already on %s as “%s”. Posting it again makes a second video there: say so to post it again.", plan.Number, plan.PlatformName, plan.As.Channel).
		WithDetails(map[string]any{"reason": "already_posted"}).WithCommand(fmt.Sprintf("metarush publish post %s --version r%d --again", plan.ProjectID, plan.Number))
}

// sentWords is how much of a post has gone ("40% sent").
func sentWords(x *models.Publication) string {
	if x.Size <= 0 {
		return "starting"
	}
	return fmt.Sprintf("%d%% sent", min(100, x.Sent*100/x.Size))
}

// publishJournal is what this computer keeps while a post goes out
// (<home>/state/publish/<publication>.json, 0600): enough to go on with it
// after MetaRush stopped part-way, through YouTube's resumable session. The
// session's address stays on this computer: never the database or the log.
type publishJournal struct {
	Publication string        `json:"publication"`
	Revision    string        `json:"revision"`
	Project     string        `json:"project"`
	Number      int           `json:"number"`
	Person      string        `json:"person"`
	RenderKey   string        `json:"renderKey"`
	SHA256      string        `json:"sha256"`
	Size        int64         `json:"size"`
	Session     string        `json:"session,omitempty"`
	Video       publish.Video `json:"video"`
}

func publishJournalPath(a *app.App, pub string) string {
	return filepath.Join(a.Cfg.StateDir(), "publish", filepath.Base(pub)+".json")
}

func (j *publishJournal) save(a *app.App) error {
	dir := filepath.Dir(publishJournalPath(a, j.Publication))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := publishJournalPath(a, j.Publication) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, publishJournalPath(a, j.Publication))
}

func loadPublishJournal(a *app.App, pub string) *publishJournal {
	b, err := os.ReadFile(publishJournalPath(a, pub))
	if err != nil {
		return nil
	}
	var j publishJournal
	if json.Unmarshal(b, &j) != nil || j.Publication != pub {
		return nil
	}
	return &j
}

// PublishBeatEvery is how often a post going out says it still is (and how
// much has gone); PublishStale is when one that stopped saying so is taken
// to have stopped (MetaRush stopped part-way).
var (
	PublishBeatEvery = 3 * time.Second
	PublishStale     = 2 * time.Minute
)

// sendPublication sends a post that is on the version as sending (a new one,
// or one going on after MetaRush stopped), as the sender holding its claim:
// its progress and a heartbeat on the version while it goes, the session kept
// on this computer, then posted or failed — recorded on the version (only
// while it is still this sender's and still sending), in the change history
// and the log, in plain words (never an error's own text, which can name the
// upload's address). A heartbeat that finds the post no longer this sender's
// (another MetaRush took it over after this one stopped answering) stops the
// upload; a video that is up then is reported as the version records it. A
// connection that drops or a busy YouTube leaves it sending with its place
// kept, to go on by itself; a person who stops it (personStops: a terminal's
// Ctrl-C) stops it.
func sendPublication(ctx context.Context, a *app.App, j *publishJournal, pub models.Publication, personStops bool) (*publish.Posted, error) {
	revs := a.DB.C(store.CRevisions)
	bg := context.WithoutCancel(ctx)
	mine := bson.M{"_id": j.Revision, "workspaceId": a.WorkspaceID(), "publications": bson.M{"$elemMatch": bson.M{"id": j.Publication, "state": models.PublicationSending, "claim": pub.Claim}}}
	uctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lost atomic.Bool
	var sent atomic.Int64
	sent.Store(pub.Sent)
	outer := publishProgress(ctx)
	uctx = publish.WithProgress(uctx, func(n, total int64) {
		sent.Store(n)
		if outer != nil {
			outer(n, total)
		}
	})
	stop := make(chan struct{})
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		t := time.NewTicker(PublishBeatEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				res, err := revs.UpdateOne(bg, mine, bson.M{"$set": bson.M{"publications.$.beat": time.Now().UTC(), "publications.$.sent": sent.Load()}})
				if err == nil && res.MatchedCount == 0 {
					lost.Store(true) // another sender has it now: stop sending
					cancel()
					return
				}
			}
		}
	}()
	video := j.Video
	video.Session = j.Session
	video.Started = func(session string) {
		j.Session = session
		if err := j.save(a); err != nil {
			fmt.Fprintf(os.Stderr, "warning: MetaRush couldn't keep the upload's place on this computer, so it can't go on with it if it stops part-way: %v\n", err)
		}
	}
	video.Mine = func() bool {
		if !lost.Load() {
			if n, err := revs.CountDocuments(bg, mine); err == nil && n == 0 {
				lost.Store(true)
			}
		}
		return !lost.Load()
	}
	if video.Session == "" {
		_ = j.save(a) // what to send, before the session exists
	}
	posted, sendErr := publish.UploadYouTube(uctx, a.Cfg, j.Person, video)
	close(stop)
	<-beating
	if lost.Load() || (posted != nil && posted.NotMine) {
		return takenOver(bg, a, j, pub, posted)
	}
	done := time.Now().UTC()
	name := firstNonEmpty(PublishPlatforms[pub.Platform], pub.Platform)
	details := map[string]any{"platform": pub.Platform, "channel": pub.Channel, "privacy": pub.Privacy, "renderId": pub.RenderID, "sha256": pub.SHA256, "publication": pub.ID}
	if j.Session != "" && pub.Sent > 0 {
		details["wentOn"] = true
	}
	if sendErr != nil {
		words := plainPublishError(sendErr)
		details["error"] = words
		var p *publish.Problem
		goesOn := errors.As(sendErr, &p) && p.Retry && !p.Gone && j.Session != "" && !(personStops && ctx.Err() != nil)
		if goesOn {
			// Its place is kept: the daemon goes on from where YouTube got to.
			if res, err := revs.UpdateOne(bg, mine, bson.M{"$set": bson.M{"publications.$.beat": done, "publications.$.sent": sent.Load(), "publications.$.error": words}}); err == nil && res.MatchedCount == 0 {
				return takenOver(bg, a, j, pub, nil)
			}
			a.Audit(bg, "publish.interrupted", "revision", j.Revision, j.Project, details)
			return nil, &publish.Problem{Msg: fmt.Sprintf("Sending stopped part-way (%s). MetaRush goes on with it by itself from where %s got to.", strings.TrimSuffix(words, "."), name), Retry: true}
		}
		if personStops && ctx.Err() != nil {
			words = "You stopped sending it before it finished."
			details["error"] = words
		}
		res, err := revs.UpdateOne(bg, mine, bson.M{"$set": bson.M{"publications.$.state": models.PublicationFailed, "publications.$.error": words,
			"publications.$.doneAt": done, "publications.$.beat": done, "publications.$.sent": sent.Load()}})
		if err == nil && res.MatchedCount == 0 {
			return takenOver(bg, a, j, pub, nil)
		}
		_ = os.Remove(publishJournalPath(a, j.Publication))
		a.Audit(bg, "publish.failed", "revision", j.Revision, j.Project, details)
		return nil, &publish.Problem{Msg: words}
	}
	res, err := revs.UpdateOne(bg, mine, bson.M{"$set": bson.M{"publications.$.state": models.PublicationPosted, "publications.$.videoId": posted.VideoID,
		"publications.$.url": posted.URL, "publications.$.captions": posted.Captions, "publications.$.notes": posted.Notes, "publications.$.sent": j.Size,
		"publications.$.doneAt": done, "publications.$.beat": done}})
	if err == nil && res.MatchedCount == 0 {
		return takenOver(bg, a, j, pub, posted)
	}
	_ = os.Remove(publishJournalPath(a, j.Publication))
	details["videoId"], details["url"] = posted.VideoID, posted.URL
	a.Audit(bg, "publish.posted", "revision", j.Revision, j.Project, details)
	if err := wentOut(bg, a, j, pub); err != nil {
		posted.Notes = append(posted.Notes, err.Error())
	}
	return posted, nil
}

// wentOut records that a post sent past the ship gate went out (D299): the
// version says so, and the receipt does, however it finished.
func wentOut(ctx context.Context, a *app.App, j *publishJournal, pub models.Publication) error {
	if len(pub.Overrides) == 0 {
		return nil
	}
	notes := make([]models.ShipOverride, 0, len(pub.Overrides))
	for _, id := range pub.Overrides {
		notes = append(notes, models.ShipOverride{ID: id})
	}
	if err := recordSentOut(ctx, a, j.Revision, notes, models.SentPost, firstNonEmpty(PublishPlatforms[pub.Platform], pub.Platform)); err != nil {
		a.Audit(ctx, "delivery.sentOut.failed", "revision", j.Revision, j.Project, map[string]any{"error": err.Error(), "what": models.SentPost})
		return fmt.Errorf(sentOutNote, j.Number)
	}
	return nil
}

// publicationOf is a post as the version records it (nil: not found).
func publicationOf(ctx context.Context, a *app.App, revisionID, pubID string) *models.Publication {
	var rev models.Revision
	if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &rev) != nil {
		return nil
	}
	for i := range rev.Publications {
		if rev.Publications[i].ID == pubID {
			return &rev.Publications[i]
		}
	}
	return nil
}

// takenOver is what a sender that no longer owns its post reports, from what
// the version records: posted (by whoever finished the same upload), still
// going on elsewhere, or marked as not posted — and when the video reached
// the platform anyway, it stays so with a note (and, past the ship gate, the
// version says it went out).
func takenOver(ctx context.Context, a *app.App, j *publishJournal, pub models.Publication, posted *publish.Posted) (*publish.Posted, error) {
	name := firstNonEmpty(PublishPlatforms[pub.Platform], pub.Platform)
	rec := publicationOf(ctx, a, j.Revision, j.Publication)
	switch {
	case rec != nil && rec.State == models.PublicationPosted && (posted == nil || rec.VideoID == posted.VideoID):
		return &publish.Posted{VideoID: rec.VideoID, URL: rec.URL, Captions: rec.Captions, Notes: rec.Notes}, nil
	case rec != nil && rec.State == models.PublicationSending:
		return nil, apperr.New(apperr.Conflict, "This post was taken over by MetaRush running in the background (this one had stopped answering), and it goes on there: the version's page shows how it goes.")
	case rec != nil && posted != nil:
		late := fmt.Sprintf("This video reached %s after all (%s), after MetaRush had marked the post as not posted. If it was posted again since, there are two videos: delete one in YouTube Studio.", name, posted.URL)
		_, _ = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": j.Revision, "workspaceId": a.WorkspaceID(), "publications.id": j.Publication},
			bson.M{"$push": bson.M{"publications.$.notes": late}, "$set": bson.M{"publications.$.lateUrl": posted.URL}})
		a.Audit(ctx, "publish.late", "revision", j.Revision, j.Project, map[string]any{"platform": pub.Platform, "publication": pub.ID, "videoId": posted.VideoID, "url": posted.URL})
		posted.Notes, posted.Late = append(posted.Notes, late), true
		if err := wentOut(ctx, a, j, pub); err != nil {
			posted.Notes = append(posted.Notes, err.Error())
		}
		return posted, nil
	case rec != nil:
		return nil, &publish.Problem{Msg: firstNonEmpty(rec.Error, "It wasn't posted.")}
	}
	return nil, &publish.Problem{Msg: "The post isn't on the version any more."}
}

// plainPublishError is a failed post in words people read (a platform's
// refusal says itself; anything else never shows its own text).
func plainPublishError(err error) string {
	var p *publish.Problem
	if errors.As(err, &p) && p.Msg != "" {
		return p.Msg
	}
	return "Sending the video failed."
}

// publishProgress is the progress line a terminal asked for (nil: none).
func publishProgress(ctx context.Context) publish.Progress { return publish.ProgressOf(ctx) }

// RunPublishRecovery goes on with posts MetaRush stopped part-way (the
// daemon, every minute): see RecoverPublications.
func RunPublishRecovery(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, r := range RecoverPublications(ctx, a, time.Now().UTC()) {
			logf("%s", r)
		}
		t.Reset(time.Minute)
	}
}

// RecoverPublications looks once at posts still marked sending whose sender
// stopped saying so (PublishStale): this computer's go on through YouTube's
// resumable session it kept, or are marked failed in plain words when there is
// none (or YouTube no longer has it); another computer's are marked failed
// after a day. It says what it did.
func RecoverPublications(ctx context.Context, a *app.App, now time.Time) []string {
	var said []string
	cur, err := a.DB.C(store.CRevisions).Find(ctx, PublishSendingFilter(a),
		options.Find().SetProjection(bson.M{"publications": 1, "projectId": 1, "number": 1}).SetLimit(200))
	if err != nil {
		return nil
	}
	var revs []models.Revision
	if cur.All(ctx, &revs) != nil {
		return nil
	}
	for _, rev := range revs {
		for _, x := range rev.Publications {
			if x.State != models.PublicationSending {
				continue
			}
			beat := x.At
			if x.Beat != nil {
				beat = *x.Beat
			}
			mine := x.Installation == "" || x.Installation == a.Cfg.Installation.ID
			switch {
			case mine && now.Sub(beat) < PublishStale:
				continue
			case !mine && now.Sub(beat) < 24*time.Hour:
				continue
			}
			// Claim it: only one looker goes on with (or gives up on) a post, and
			// the sender that stopped answering no longer owns it.
			claimed, claim := now, ids.New("clm")
			was := bson.M{"id": x.ID, "state": models.PublicationSending, "beat": x.Beat}
			if x.Claim == "" {
				was["claim"] = bson.M{"$exists": false}
			} else {
				was["claim"] = x.Claim
			}
			res, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev.ID, "workspaceId": a.WorkspaceID(), "publications": bson.M{"$elemMatch": was}},
				bson.M{"$set": bson.M{"publications.$.beat": claimed, "publications.$.claim": claim}, "$inc": bson.M{"publications.$.tries": 1}})
			if err != nil || res.MatchedCount == 0 {
				continue
			}
			x.Claim = claim
			version := fmt.Sprintf("version %d", rev.Number)
			j := (*publishJournal)(nil)
			if mine {
				j = loadPublishJournal(a, x.ID)
			}
			if j == nil || j.Session == "" {
				why := "MetaRush stopped part-way through sending it, and this computer kept no place to go on from. Check YouTube Studio; post it again if it isn't there."
				if !mine {
					why = "It stopped part-way on another computer, which hasn't gone on with it for a day. Check YouTube Studio; post it again if it isn't there."
				}
				failPublication(ctx, a, rev.ID, rev.ProjectID, x, why)
				said = append(said, fmt.Sprintf("posting %s to %s stopped part-way and can't go on: marked as not posted", version, PublishPlatforms[x.Platform]))
				continue
			}
			if x.Tries >= PublishTries || now.Sub(x.At) > 23*time.Hour {
				tries := "once"
				if x.Tries > 0 {
					tries = fmt.Sprintf("%d times", x.Tries+1)
				}
				failPublication(ctx, a, rev.ID, rev.ProjectID, x, fmt.Sprintf("%s kept failing to take it (tried %s over %s), so MetaRush stopped trying. Check YouTube Studio; post it again if it isn't there.",
					firstNonEmpty(PublishPlatforms[x.Platform], x.Platform), tries, durationWords(now.Sub(x.At))))
				said = append(said, fmt.Sprintf("posting %s to %s kept failing: marked as not posted", version, PublishPlatforms[x.Platform]))
				continue
			}
			// Nothing goes on that a person couldn't post now: the version's
			// approval, the ship gate, the client's rules and the sign-in are
			// asked again, as the person who posted it.
			if holds, err := stillPostable(ctx, a, rev, x); err != nil {
				_, _ = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev.ID, "workspaceId": a.WorkspaceID(), "publications.id": x.ID}, bson.M{"$set": bson.M{"publications.$.beat": x.Beat}})
				continue // asked again at the next look
			} else if len(holds) > 0 {
				failPublication(ctx, a, rev.ID, rev.ProjectID, x, "MetaRush didn't go on with it after it stopped part-way: "+lowerFirst(holds[0]))
				said = append(said, fmt.Sprintf("posting %s to %s stopped part-way and something holds it now: marked as not posted", version, PublishPlatforms[x.Platform]))
				continue
			}
			src, err := a.Cache.Ensure(ctx, a.Media, j.RenderKey, j.SHA256, j.Size, nil)
			if err != nil {
				// The file isn't here now: try again at the next look.
				_, _ = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev.ID, "workspaceId": a.WorkspaceID(), "publications.id": x.ID}, bson.M{"$set": bson.M{"publications.$.beat": x.Beat}})
				continue
			}
			j.Video.Path, j.Video.Size = src, j.Size
			x.Beat = &claimed
			_, err = sendPublication(ctx, a, j, x, false)
			var p *publish.Problem
			switch {
			case err == nil:
				said = append(said, fmt.Sprintf("went on posting %s to %s after MetaRush stopped part-way: posted", version, PublishPlatforms[x.Platform]))
			case errors.As(err, &p) && p.Gone:
				said = append(said, fmt.Sprintf("posting %s to %s stopped part-way and YouTube no longer has it: marked as not posted", version, PublishPlatforms[x.Platform]))
			default:
				said = append(said, fmt.Sprintf("going on posting %s to %s failed: %s", version, PublishPlatforms[x.Platform], err.Error()))
			}
		}
	}
	return said
}

// PublishTries is how many times MetaRush goes on with a post that stopped
// part-way before it gives up (YouTube keeps an unfinished upload about a day).
var PublishTries = 8

// durationWords says how long something took, roughly (40 minutes, 3 hours).
func durationWords(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return "a minute"
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

// PublishSendingFilter finds versions with a post still sending (migration
// 60's partial index answers it).
func PublishSendingFilter(a *app.App) bson.M {
	return bson.M{"workspaceId": a.WorkspaceID(), "publications.state": models.PublicationSending}
}

// stillPostable asks the plan again, as the person who posted it, for what
// would hold the post now (its own going out aside).
func stillPostable(ctx context.Context, a *app.App, rev models.Revision, x models.Publication) ([]string, error) {
	as := *a
	as.Actor = x.By
	plan, _, _, err := publishPlan(ctx, &as, &PublishIn{ProjectID: rev.ProjectID, Revision: rev.ID, Platform: x.Platform, Privacy: x.Privacy, Preset: x.Preset}, true)
	if err != nil {
		return nil, err
	}
	var holds []string
	for _, h := range plan.Holds {
		if !h.Sending {
			holds = append(holds, h.Says)
		}
	}
	return holds, nil
}

// failPublication marks a post that can't go on as failed, saying why, and
// tells the person who posted it (the bell).
func failPublication(ctx context.Context, a *app.App, revID, projectID string, x models.Publication, why string) {
	done := time.Now().UTC()
	was := bson.M{"id": x.ID, "state": models.PublicationSending}
	if x.Claim != "" {
		was["claim"] = x.Claim
	}
	res, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": revID, "workspaceId": a.WorkspaceID(), "publications": bson.M{"$elemMatch": was}},
		bson.M{"$set": bson.M{"publications.$.state": models.PublicationFailed, "publications.$.error": why, "publications.$.doneAt": done}})
	if err != nil || res.MatchedCount == 0 {
		return
	}
	_ = os.Remove(publishJournalPath(a, x.ID))
	a.Audit(ctx, "publish.failed", "revision", revID, projectID, map[string]any{"platform": x.Platform, "channel": x.Channel, "publication": x.ID, "error": why, "stoppedPartWay": true})
	var rev models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revID, &rev)
	name := firstNonEmpty(PublishPlatforms[x.Platform], x.Platform)
	_, _ = notify.Upsert(ctx, a, models.Notification{UserID: x.By.UserID, Kind: models.NotifyPublishStopped, GroupKey: "publish:" + x.ID, Tone: "error",
		Title: fmt.Sprintf("Version %d wasn't posted to %s", rev.Number, name), Body: why, ProjectID: projectID, Link: links.Revision(projectID, revID),
		Details: map[string]any{"publication": x.ID, "platform": x.Platform}}, notify.Once)
}

// PublishOpener shows a platform's sign-in page; tests set a stand-in (the
// real browser is never opened under go test).
var PublishOpener publish.Opener

// publishOpener opens the sign-in page through the allowlisted opener.
func publishOpener(_ context.Context, a *app.App) publish.Opener {
	if PublishOpener != nil {
		return PublishOpener
	}
	return func(ctx context.Context, u string) (func(), error) {
		if testing.Testing() {
			return func() {}, errors.New("the browser is never opened in tests")
		}
		return a.Tools.OpenURL(ctx, u)
	}
}

// publishErr turns a platform's refusal into MetaRush's error.
func publishErr(err error, name string) error {
	var p *publish.Problem
	switch {
	case errors.Is(err, publish.ErrNotSetUp):
		return apperr.New(apperr.NotConfigured, "%s isn't set up on this computer yet.", name).WithHint("An owner registers MetaRush with their own Google Cloud project once (docs/publishing.md).").WithCommand("metarush publish setup youtube")
	case errors.Is(err, publish.ErrNotSignedIn):
		return apperr.New(apperr.NotConfigured, "You aren't signed in to %s on this computer.", name).WithHint("Sign in with your own channel, at this computer.").WithCommand("metarush publish connect youtube")
	case errors.As(err, &p):
		e := apperr.New(apperr.Provider, "%s", p.Msg)
		if p.Hint != "" {
			e = e.WithHint(p.Hint)
		}
		if p.Command != "" {
			e = e.WithCommand(p.Command)
		}
		return e
	}
	return err
}

// First-party ability (D269).
func init() {
	Offer("publish.send", Offering{ID: "post-to-youtube", Group: "deliver", Title: "Post to YouTube",
		Does:  "Posts an approved version to your own channel as you, with its title, description, chapters and captions; a person does it every time.",
		Words: []string{"post my video online", "upload to my channel", "share it publicly"},
		Needs: Needs{Leaves: "the version's video, title, description and captions leave this computer, for YouTube"}})
}
