package providers

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// ListOut lists integrations and their status on this machine.
type ListOut struct {
	Providers []Status `json:"providers"`
}

// NameIn selects a provider.
type NameIn struct {
	Name string `json:"name" jsonschema:"enum=elevenlabs,enum=gemini"`
}

// StatusIn asks for one provider's status (optionally with a project's settings and budget).
type StatusIn struct {
	Name      string `json:"name" jsonschema:"enum=elevenlabs,enum=gemini"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Refresh   bool   `json:"refresh,omitempty" jsonschema:"description=Ask the provider for live plan usage (tests the stored key)."`
}

// BudgetStatus is a project's spending against its budget.
type BudgetStatus struct {
	LimitCredits     *float64 `json:"limitCredits,omitempty"`
	UsedCredits      float64  `json:"usedCredits"`
	RemainingCredits *float64 `json:"remainingCredits,omitempty"`
}

// StatusOut is a provider's status with the shared settings.
type StatusOut struct {
	Status Status `json:"status"`
	// Settings are the narration voice choices of the Content Settings in use
	// (D193): the project's (its own, another project's or the default's), or
	// without a project the default's. Never a budget.
	Settings *models.ProviderSettings `json:"settings,omitempty"`
	// From is the project whose Content Settings hold them; ReadOnly: the
	// project asked about uses another project's (change them there).
	From     *ops.ContentProjectRef `json:"from,omitempty"`
	ReadOnly bool                   `json:"readOnly,omitempty"`
	// UsedBy counts the other projects using the same settings.
	UsedBy int `json:"usedBy,omitempty"`
	// Project is the project's own provider settings: its budget and credits used.
	Project   *models.ProviderSettings `json:"project,omitempty"`
	Effective ResolvedVoice            `json:"effective"`
	Budget    *BudgetStatus            `json:"budget,omitempty"`
}

// SetIn stores a key for this machine. The key is never returned or logged.
type SetIn struct {
	Name                  string `json:"name" jsonschema:"enum=elevenlabs,enum=gemini"`
	Key                   string `json:"key" jsonschema:"minLength=16,maxLength=512,description=The API key. Sent once; never shown again."`
	SkipTest              bool   `json:"skipTest,omitempty" jsonschema:"description=Save without checking the key with the provider (offline)."`
	AllowUnsafeSecretPath bool   `json:"allowUnsafeSecretPath,omitempty" jsonschema:"description=Env-mode installations: allow <home>/.env inside a repository that ignores it."`
}

// TestIn tests the stored key, or a candidate key without storing it.
type TestIn struct {
	Name string `json:"name" jsonschema:"enum=elevenlabs,enum=gemini"`
	Key  string `json:"key,omitempty" jsonschema:"maxLength=512,description=A key to check before saving it (not stored)."`
}

// TestOut is a test result.
type TestOut struct {
	Result TestResult `json:"result"`
	Status Status     `json:"status"`
	Tested string     `json:"tested" jsonschema:"enum=stored,enum=candidate"`
}

// SettingsIn changes the shared, non-secret provider choices: the narration
// voice choices of a project's own Content Settings (D193; without a project,
// the default's — what new projects use) and a project's budget. A project
// that uses another's settings can change its budget, not the voice. Unset
// fields stay as they are; Clear lists fields to reset.
type SettingsIn struct {
	Name            string   `json:"name" jsonschema:"enum=elevenlabs"`
	Scope           string   `json:"scope,omitempty" jsonschema:"enum=workspace,enum=project,description=project (with projectId: its own settings and budget) or workspace (the default's settings). Default: project when projectId is given."`
	ProjectID       string   `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Voice           string   `json:"voice,omitempty" jsonschema:"maxLength=120,description=Voice ID or name (from voiceover voices)."`
	LibraryOwnerID  string   `json:"libraryOwnerId,omitempty" jsonschema:"maxLength=120,description=For a voice from the public library: its owner ID (the voice is added to the account first)."`
	ModelID         string   `json:"modelId,omitempty" jsonschema:"maxLength=120"`
	Stability       *float64 `json:"stability,omitempty" jsonschema:"minimum=0,maximum=1"`
	SimilarityBoost *float64 `json:"similarityBoost,omitempty" jsonschema:"minimum=0,maximum=1"`
	Style           *float64 `json:"style,omitempty" jsonschema:"minimum=0,maximum=1"`
	Speed           *float64 `json:"speed,omitempty" jsonschema:"minimum=0.7,maximum=1.2"`
	SpeakerBoost    *bool    `json:"speakerBoost,omitempty"`
	OutputFormat    string   `json:"outputFormat,omitempty" jsonschema:"maxLength=40"`
	Region          *string  `json:"region,omitempty" jsonschema:"enum=,enum=us,enum=eu,enum=in,enum=sg"`
	BudgetCredits   *float64 `json:"budgetCredits,omitempty" jsonschema:"minimum=0,description=Project scope: the most this project may spend (credits ≈ characters). 0 removes the limit."`
	Favorite        string   `json:"favorite,omitempty" jsonschema:"maxLength=120,description=Mark a voice (ID) as a favourite of these settings."`
	Unfavorite      string   `json:"unfavorite,omitempty" jsonschema:"maxLength=120"`
	Clear           []string `json:"clear,omitempty" jsonschema:"description=Fields to reset: voice\\, model\\, stability\\, similarityBoost\\, style\\, speed\\, speakerBoost\\, outputFormat."`
}

// SettingsOut is the result of a settings change.
type SettingsOut struct {
	StatusOut
	Warnings []string `json:"warnings,omitempty"`
	// Note says whose settings changed when it isn't obvious: the default's
	// (what new projects use), or settings other projects use too.
	Note string `json:"note,omitempty"`
}

func init() {
	ops.Def("provider.list", "List integrations (API-key providers such as ElevenLabs) and whether this machine has a key", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*ListOut, error) {
			out := &ListOut{Providers: []Status{}}
			for _, p := range All() {
				out.Providers = append(out.Providers, StatusFor(a.Cfg, p))
			}
			return out, nil
		})
	ops.Def("provider.status", "Show one integration: key status on this machine (masked), last test, plan usage, shared voice/model settings and a project's budget", ops.Flags{},
		func(ctx context.Context, a *app.App, in *StatusIn) (*StatusOut, error) {
			p, err := Get(in.Name)
			if err != nil {
				return nil, err
			}
			if in.Refresh {
				if key, _, err := Key(a.Cfg, p.Name()); err == nil && key != "" {
					if r, err := p.Test(ctx, key); err == nil {
						RecordTest(a.Cfg, p.Name(), MaskFor(p.Name(), key), r)
					}
				}
			}
			return statusOut(ctx, a, p, in.ProjectID)
		})
	ops.Def("provider.set", "Save this machine's API key for an integration (keychain, or <home>/.env for env-mode installations); tested first unless skipTest; owners", ops.Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *SetIn) (*Status, error) {
			p, err := Get(in.Name)
			if err != nil {
				return nil, err
			}
			key, err := ShapeKey(p.Name(), strings.TrimSpace(in.Key))
			in.Key = ""
			if err != nil {
				return nil, err
			}
			var res *TestResult
			if !in.SkipTest {
				res, err = p.Test(ctx, key)
				if err != nil {
					return nil, apperr.Wrap(apperr.Provider, err, "MetaRush couldn't check the key with %s.", p.Title()).
						WithHint("Try again when you're online, or save it without checking.").WithCommand("metarush providers set " + p.Name() + " --no-test")
				}
				if !res.OK {
					return nil, apperr.New(apperr.Provider, "%s", res.Message)
				}
			}
			src, err := StoreKey(a.Cfg, p.Name(), key, in.AllowUnsafeSecretPath)
			if err != nil {
				return nil, err
			}
			recordSet(a.Cfg, p.Name(), MaskFor(p.Name(), key), res)
			a.Audit(ctx, "provider.set", "provider", p.Name(), "", map[string]any{"provider": p.Name(), "storedIn": src, "machine": a.Cfg.Installation.Name, "tested": res != nil})
			st := StatusFor(a.Cfg, p)
			return &st, nil
		})
	ops.Def("provider.test", "Check the stored key (or a candidate key, without saving it) with the provider", ops.Flags{},
		func(ctx context.Context, a *app.App, in *TestIn) (*TestOut, error) {
			p, err := Get(in.Name)
			if err != nil {
				return nil, err
			}
			key, tested := strings.TrimSpace(in.Key), "candidate"
			in.Key = ""
			if key == "" {
				if key, err = MustKey(a.Cfg, p.Name()); err != nil {
					return nil, err
				}
				tested = "stored"
			} else if key, err = ShapeKey(p.Name(), key); err != nil {
				return nil, err
			}
			r, err := p.Test(ctx, key)
			if err != nil {
				return nil, err
			}
			if tested == "stored" {
				RecordTest(a.Cfg, p.Name(), MaskFor(p.Name(), key), r)
			}
			a.Audit(ctx, "provider.test", "provider", p.Name(), "", map[string]any{"provider": p.Name(), "ok": r.OK, "tested": tested, "machine": a.Cfg.Installation.Name})
			return &TestOut{Result: *r, Status: StatusFor(a.Cfg, p), Tested: tested}, nil
		})
	ops.Def("provider.remove", "Remove this machine's API key for an integration; owners", ops.Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *NameIn) (*Status, error) {
			p, err := Get(in.Name)
			if err != nil {
				return nil, err
			}
			removed, err := RemoveKey(a.Cfg, p.Name())
			if err != nil {
				return nil, err
			}
			forget(a.Cfg, p.Name())
			if removed {
				a.Audit(ctx, "provider.remove", "provider", p.Name(), "", map[string]any{"provider": p.Name(), "machine": a.Cfg.Installation.Name})
			}
			st := StatusFor(a.Cfg, p)
			return &st, nil
		})
	ops.Def("provider.settings", "Change the narration voice of a project's own Content Settings (without a project, the default's): the voice, model, voice settings, region and favourite voices; or a project's budget", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SettingsIn) (*SettingsOut, error) {
			return changeSettings(ctx, a, in)
		})
}

// voiceChoiceFields clears what isn't money from a project's provider
// settings: its voice choices are in its Content Settings (D193).
func budgetOnly(ps models.ProviderSettings) *models.ProviderSettings {
	return &models.ProviderSettings{BudgetCredits: ps.BudgetCredits, UsedCredits: ps.UsedCredits, BudgetUSD: ps.BudgetUSD, DailyCapUSD: ps.DailyCapUSD,
		Model: ps.Model, DraftResolution: ps.DraftResolution, FinalResolution: ps.FinalResolution, UpdatedAt: ps.UpdatedAt, UpdatedBy: ps.UpdatedBy}
}

// LoadSettings reads the narration voice choices of the Content Settings a
// project uses (D193; projectID "": the default's) and the project's own
// provider settings — its budget and credits used (never voice choices).
func LoadSettings(ctx context.Context, a *app.App, name, projectID string) (choices, proj *models.ProviderSettings, project *models.Project, err error) {
	if projectID != "" {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
			return nil, nil, nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": projectID})
		}
		project = &p
		if s, ok := p.Providers[name]; ok {
			proj = budgetOnly(s)
		}
	}
	rec, _, err := content.For(ctx, a, projectID)
	if err != nil {
		return nil, nil, nil, err
	}
	if v, ok := rec.Voice[name]; ok {
		choices = v.Provider()
	}
	return choices, proj, project, nil
}

func budgetOf(proj *models.ProviderSettings) *BudgetStatus {
	b := &BudgetStatus{}
	if proj == nil {
		return b
	}
	b.UsedCredits = math.Round(proj.UsedCredits*10) / 10
	if proj.BudgetCredits != nil && *proj.BudgetCredits > 0 {
		l := *proj.BudgetCredits
		r := math.Max(0, l-proj.UsedCredits)
		b.LimitCredits, b.RemainingCredits = &l, &r
	}
	return b
}

func statusOut(ctx context.Context, a *app.App, p Provider, projectID string) (*StatusOut, error) {
	ws, proj, _, err := LoadSettings(ctx, a, p.Name(), projectID)
	if err != nil {
		return nil, err
	}
	out := &StatusOut{Status: StatusFor(a.Cfg, p), Settings: ws, Project: proj, Effective: ResolveVoice(ws, proj, nil)}
	if g, r, err := content.Resolve(ctx, a, projectID); err == nil && r.Owner != "" {
		out.From = &ops.ContentProjectRef{ID: r.Owner, Name: g.Name(r.Owner), Archived: g.Archived(r.Owner)}
		out.ReadOnly = projectID != "" && r.Owner != projectID
		for _, id := range g.UsedBy(r.Owner) {
			if id != projectID {
				out.UsedBy++
			}
		}
	}
	if projectID != "" {
		out.Budget = budgetOf(proj)
	}
	return out, nil
}

// ElevenClientFor builds a client with this machine's key and the region of
// the default's Content Settings.
func ElevenClientFor(ctx context.Context, a *app.App) (*ElevenClient, error) {
	return ElevenClientForProject(ctx, a, "")
}

// ElevenClientForProject builds a client with this machine's key and the
// region of the Content Settings a project uses.
func ElevenClientForProject(ctx context.Context, a *app.App, projectID string) (*ElevenClient, error) {
	key, err := MustKey(a.Cfg, ElevenLabsName)
	if err != nil {
		return nil, err
	}
	ws, _, _, _ := LoadSettings(ctx, a, ElevenLabsName, projectID)
	region := ""
	if ws != nil {
		region = ws.Region
	}
	return NewElevenClient(key, region)
}

func changeSettings(ctx context.Context, a *app.App, in *SettingsIn) (*SettingsOut, error) {
	p, err := Get(in.Name)
	if err != nil {
		return nil, err
	}
	if in.BudgetCredits != nil && !ops.CallerFrom(ctx).Human {
		// A project's budget is a person's to set (D241: Auto spends only
		// within limits a person set, and who set it is kept).
		return nil, apperr.New(apperr.HumanOnly, "Only a person can set a project's budget.").
			WithHint("Set it in Settings → Integrations, or yourself in a terminal.").WithDetails(map[string]any{"op": "provider.settings", "part": "budgetCredits"})
	}
	if p.Category() != CategoryVoice {
		return nil, apperr.New(apperr.InvalidInput, "%s's settings (the model, quality and spending limits) are in Settings, Integrations, under Generative video.", p.Title()).
			WithCommand("metarush generate configure")
	}
	scope := in.Scope
	if scope == "" {
		scope = "workspace"
		if in.ProjectID != "" {
			scope = "project"
		}
	}
	if scope == "project" && in.ProjectID == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose the project these settings are for.")
	}
	// Without a project the voice choices are the default project's Content
	// Settings (D193), which any member may change like a project's own
	// (decision C); keys and the team's money stay the owners' (D191).
	if scope == "workspace" && in.BudgetCredits != nil {
		return nil, apperr.New(apperr.InvalidInput, "Budgets are set for each project. Choose the project.")
	}
	voiceChange := in.Voice != "" || in.ModelID != "" || in.Stability != nil || in.SimilarityBoost != nil || in.Style != nil || in.Speed != nil ||
		in.SpeakerBoost != nil || in.OutputFormat != "" || in.Region != nil || in.Favorite != "" || in.Unfavorite != "" || len(in.Clear) > 0
	// The voice choices are the Content Settings' (D193): a project's own, or
	// without a project the default's. A project that uses another project's
	// settings can't change them from here.
	target := &content.Target{}
	var graph *content.Graph
	if voiceChange {
		pid := ""
		if scope == "project" {
			pid = in.ProjectID
		}
		var err error
		if target, graph, err = content.EditTarget(ctx, a, pid); err != nil {
			return nil, err
		}
	}
	var warnings []string
	set, unset := bson.M{}, bson.M{}
	prefix := "voice." + p.Name() + "."
	var client *ElevenClient
	needClient := in.Voice != "" || in.ModelID != ""
	if needClient {
		if client, err = ElevenClientFor(ctx, a); err != nil {
			return nil, err
		}
	}
	if in.Voice != "" {
		v, err := findVoice(ctx, client, in.Voice, in.LibraryOwnerID)
		if err != nil {
			return nil, err
		}
		set[prefix+"voiceId"], set[prefix+"voiceName"] = v.VoiceID, v.Name
	}
	var model *Model
	if in.ModelID != "" {
		ms, err := client.Models(ctx)
		if err != nil {
			return nil, err
		}
		if model = FindModel(ms, in.ModelID); model == nil || !model.TTS {
			return nil, apperr.New(apperr.InvalidInput, "Your ElevenLabs account has no model “%s” that turns text into speech.", in.ModelID).
				WithHint("Choose a model from the list in Settings → Integrations.").WithCommand("metarush voiceover models")
		}
		set[prefix+"modelId"], set[prefix+"modelName"] = model.ModelID, model.Name
	}
	num := func(field string, v *float64, lo, hi float64) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return apperr.New(apperr.InvalidInput, "%s must be between %g and %g.", settingWords[field], lo, hi).WithDetails(map[string]any{"setting": field})
		}
		set[prefix+"voice."+field] = math.Round(*v*1000) / 1000
		return nil
	}
	for _, c := range []struct {
		f      string
		v      *float64
		lo, hi float64
	}{{"stability", in.Stability, 0, 1}, {"similarityBoost", in.SimilarityBoost, 0, 1}, {"style", in.Style, 0, 1}, {"speed", in.Speed, 0.7, 1.2}} {
		if err := num(c.f, c.v, c.lo, c.hi); err != nil {
			return nil, err
		}
	}
	if in.SpeakerBoost != nil {
		set[prefix+"voice.speakerBoost"] = *in.SpeakerBoost
	}
	if in.OutputFormat != "" {
		if !validOutputFormat(in.OutputFormat) {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush doesn't know the audio format “%s”.", in.OutputFormat).
				WithDetails(map[string]any{"examples": []string{"mp3_44100_128", "mp3_44100_192", "wav_44100"}})
		}
		set[prefix+"voice.outputFormat"] = in.OutputFormat
	}
	if in.Region != nil {
		if _, err := elevenRegionBase(*in.Region); err != nil && *in.Region != "" {
			return nil, err
		}
		set[prefix+"region"] = *in.Region
	}
	budgetSet, budgetUnset := bson.M{}, bson.M{}
	if in.BudgetCredits != nil {
		bp := "providers." + p.Name() + ".budgetCredits"
		if *in.BudgetCredits <= 0 {
			budgetUnset[bp], budgetUnset[bp+"By"] = "", ""
		} else {
			budgetSet[bp] = math.Round(*in.BudgetCredits)
			budgetSet[bp+"By"] = models.SetNow(a.Actor) // who set it (D241)
		}
	}
	for _, c := range in.Clear {
		switch c {
		case "voice":
			unset[prefix+"voiceId"], unset[prefix+"voiceName"] = "", ""
		case "model":
			unset[prefix+"modelId"], unset[prefix+"modelName"] = "", ""
		case "stability", "similarityBoost", "style", "speed", "speakerBoost", "outputFormat":
			unset[prefix+"voice."+c] = ""
		default:
			return nil, apperr.New(apperr.InvalidInput, "There is no voice setting “%s” to reset.", c).
				WithDetails(map[string]any{"settings": []string{"voice", "model", "stability", "similarityBoost", "style", "speed", "speakerBoost", "outputFormat"}})
		}
	}
	now := time.Now().UTC()
	actor := a.Actor
	if voiceChange {
		set[prefix+"updatedAt"], set[prefix+"updatedBy"] = now, actor
		update := bson.M{"$set": set}
		if len(unset) > 0 {
			update["$unset"] = unset
		}
		if in.Favorite != "" {
			if client == nil {
				if c, err := ElevenClientFor(ctx, a); err == nil {
					client = c
				}
			}
			fav := models.FavoriteVoice{VoiceID: in.Favorite}
			if client != nil {
				if v, err := findVoice(ctx, client, in.Favorite, ""); err == nil {
					fav = models.FavoriteVoice{VoiceID: v.VoiceID, Name: v.Name}
				}
			}
			update["$addToSet"] = bson.M{prefix + "favorites": fav}
		}
		if err := content.Update(ctx, a, target.ProjectID, update); err != nil {
			return nil, err
		}
		if in.Unfavorite != "" {
			_ = content.Update(ctx, a, target.ProjectID, bson.M{"$pull": bson.M{prefix + "favorites": bson.M{"voiceId": in.Unfavorite}}})
		}
	}
	if len(budgetSet) > 0 || len(budgetUnset) > 0 {
		budgetSet["providers."+p.Name()+".updatedAt"], budgetSet["providers."+p.Name()+".updatedBy"] = now, actor
		update := bson.M{"$set": budgetSet}
		if len(budgetUnset) > 0 {
			update["$unset"] = budgetUnset
		}
		if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, update); err != nil {
			return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": in.ProjectID})
		}
	}
	changed := []string{}
	for k := range set {
		changed = append(changed, strings.TrimPrefix(k, prefix))
	}
	for k := range unset {
		changed = append(changed, "-"+strings.TrimPrefix(k, prefix))
	}
	for k := range budgetSet {
		if strings.HasSuffix(k, ".budgetCredits") {
			changed = append(changed, "budgetCredits")
		}
	}
	if len(budgetUnset) > 0 {
		changed = append(changed, "-budgetCredits")
	}
	if in.Favorite != "" {
		changed = append(changed, "+favorite:"+in.Favorite)
	}
	if in.Unfavorite != "" {
		changed = append(changed, "-favorite:"+in.Unfavorite)
	}
	sort.Strings(changed)
	a.Audit(ctx, "provider.settings", "provider", p.Name(), in.ProjectID, map[string]any{"provider": p.Name(), "scope": scope, "fields": changed, "settingsOf": target.ProjectID})
	so, err := statusOut(ctx, a, p, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if model != nil {
		eff := so.Effective
		if w, err := ValidateVoice(eff, []Model{*model}, ""); err == nil {
			warnings = append(warnings, w...)
		}
	}
	out := &SettingsOut{StatusOut: *so, Warnings: warnings}
	if voiceChange && graph != nil {
		out.Note = target.Note(graph)
	}
	return out, nil
}

// settingWords names the voice settings the way Settings → Integrations does.
var settingWords = map[string]string{"stability": "Delivery", "similarityBoost": "Closeness to the original voice", "style": "Speaking style", "speed": "Speed"}

func validOutputFormat(f string) bool {
	switch f {
	case "mp3_22050_32", "mp3_24000_48", "mp3_44100_32", "mp3_44100_64", "mp3_44100_96", "mp3_44100_128", "mp3_44100_192",
		"wav_22050", "wav_24000", "wav_44100", "wav_48000", "pcm_22050", "pcm_24000", "pcm_44100", "pcm_48000",
		"opus_48000_64", "opus_48000_96", "opus_48000_128", "opus_48000_192":
		return true
	}
	return false
}

// findVoice finds a voice by ID or (case-insensitive) name in the account, or
// adds a library voice to the account when its owner is given.
func findVoice(ctx context.Context, c *ElevenClient, ref, libraryOwner string) (*Voice, error) {
	if libraryOwner != "" {
		lib, err := c.SharedVoices(ctx, "", 0, 100)
		name := ref
		if err == nil {
			for _, v := range lib.Voices {
				if v.VoiceID == ref {
					name = v.Name
				}
			}
		}
		id, err := c.AddSharedVoice(ctx, libraryOwner, ref, name)
		if err != nil {
			return nil, err
		}
		return &Voice{VoiceID: id, Name: name}, nil
	}
	if page, err := c.Voices(ctx, "", "", 100, []string{ref}); err == nil {
		for _, v := range page.Voices {
			if v.VoiceID == ref {
				return &v, nil
			}
		}
	}
	page, err := c.Voices(ctx, ref, "", 100, nil)
	if err != nil {
		return nil, err
	}
	var hits []Voice
	for _, v := range page.Voices {
		if strings.EqualFold(v.Name, ref) || v.VoiceID == ref {
			return &v, nil
		}
		if strings.Contains(strings.ToLower(v.Name), strings.ToLower(ref)) {
			hits = append(hits, v)
		}
	}
	if len(hits) == 1 {
		return &hits[0], nil
	}
	if len(hits) > 1 {
		names := []string{}
		for _, h := range hits {
			names = append(names, h.Name)
		}
		return nil, apperr.New(apperr.InvalidInput, "Several voices match “%s”: %s.", ref, strings.Join(names, ", ")).WithHint("Use the voice's full name.")
	}
	return nil, apperr.New(apperr.NotFound, "Your ElevenLabs account has no voice called “%s”.", ref).
		WithHint("Pick a voice in Settings → Integrations, where you can also search ElevenLabs' public library.").WithCommand("metarush voiceover voices --library")
}

// ResolveFor resolves the voice for one line of a project's script.
func ResolveFor(ctx context.Context, a *app.App, projectID string, beat *editspec.VoiceRef) (ResolvedVoice, error) {
	ws, proj, _, err := LoadSettings(ctx, a, ElevenLabsName, projectID)
	if err != nil {
		return ResolvedVoice{}, err
	}
	return ResolveVoice(ws, proj, beat), nil
}

// ChargeProject records spending against a project's budget.
func ChargeProject(ctx context.Context, a *app.App, projectID string, credits float64) {
	if credits <= 0 || projectID == "" {
		return
	}
	_ = a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$inc": bson.M{"providers." + ElevenLabsName + ".usedCredits": credits}})
}

// ProjectBudget returns the project's budget status.
func ProjectBudget(ctx context.Context, a *app.App, projectID string) (*BudgetStatus, error) {
	_, proj, _, err := LoadSettings(ctx, a, ElevenLabsName, projectID)
	if err != nil {
		return nil, err
	}
	return budgetOf(proj), nil
}
