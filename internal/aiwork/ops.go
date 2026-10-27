package aiwork

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

func init() {
	ops.Def("ai.models", "List the AI services' models for each kind of work — pictures, voices, voice copies, voice change, sound effects, music, dubbing, transcription and video — with dated prices, licence and data terms, and what a project's Content Settings allow", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ModelsIn) (*ModelsOut, error) { return modelsOut(ctx, a, in) })
	ops.Def("ai.estimate", "Price AI work — how much a picture, speech, a sound effect, music, a dub or a cloud transcript would cost — and check it may be made (the model and why, up to two alternatives, consent, budget, this computer's key, licence terms); nothing is sent or spent", ops.Flags{},
		func(ctx context.Context, a *app.App, in *MakeIn) (*AIEstimate, error) {
			p, err := planMake(leave.Quiet(ctx), a, in) // an estimate: a refusal is said, not recorded (D290)
			if err != nil {
				return nil, err
			}
			e := p.estimate(ctx, a)
			return &e, nil
		})
	ops.Def("ai.make", "Make a picture, speech of a script, a sound effect, music, a clip in another voice, a dub or a cloud transcript with an AI service; priced first: needs maxUsd, the agreed estimate, unless your estimates are off", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *MakeIn) (*MakeOut, error) {
			pctx := ctx
			if in.DryRun {
				pctx = leave.Quiet(ctx)
			}
			p, err := planMake(pctx, a, in)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
		})
	ops.Def("ai.list", "List AI work (pictures, voices, sound effects, music, dubs, transcripts, voice copies) with what it cost", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ListIn) (*ListOut, error) { return listWork(ctx, a, in) })
	ops.Def("ai.show", "Show how a piece of AI work was made: the model and why, what was sent, settings, licence and data terms, cost, consent and what it made", ops.Flags{},
		func(ctx context.Context, a *app.App, in *IDIn) (*WorkView, error) {
			g, err := resolve(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			v := views(ctx, a, []models.Generation{*g})[0]
			return &v, nil
		})
	ops.Def("ai.cancel", "Stop AI work that is waiting or being made", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *IDIn) (*CancelOut, error) { return cancelWork(ctx, a, in.ID) })
	ops.Def("ai.voices", "List the voice copies MetaRush made, whose voice each is and whether it may still be used", ops.Flags{},
		func(ctx context.Context, a *app.App, in *VoicesIn) (*VoicesOut, error) { return listVoices(ctx, a, in) })
	ops.Def("ai.voices.copy", "Copy a person's voice with an AI service, with their consent in force (and, where the service wants it, a recording of them reading its consent script) — people only, priced first", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *CopyIn) (*MakeOut, error) {
			pctx := ctx
			if in.DryRun {
				pctx = leave.Quiet(ctx) // a dry run: a refusal is said, not recorded (D290)
			}
			p, err := planCopy(pctx, a, in)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, "")
		})
	ops.Def("ai.voices.remove", "Withdraw a voice copy: deleted at the provider, and nothing more is said with it (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *VoiceRemoveIn) (*VoiceRemoveOut, error) {
			return removeVoice(ctx, a, in)
		})
	ops.Def("ai.rules", "Show which AI services a project's work may go to (the AI services part of its Content Settings)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RulesIn) (*RulesOut, error) {
			return rulesOut(ctx, a, in.ProjectID)
		})
	ops.Def("ai.rules.set", "Change which AI services the work of projects using a project's own Content Settings may go to: none at all, never some providers, or a model chosen for a kind of work (people only: like budgets, a client's rule is a person's decision)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RulesSetIn) (*RulesOut, error) { return rulesSet(ctx, a, in) })
}

func modelsOut(ctx context.Context, a *app.App, in *ModelsIn) (*ModelsOut, error) {
	now := time.Now().UTC()
	aiservices.Refresh(ctx, a) // abilities that provide a kind of work (D281)
	rules, _, err := rulesFor(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	out := &ModelsOut{Kinds: []KindView{}, Providers: []ProviderView{}}
	total := 0
	for _, k := range aiservices.Kinds() {
		if in.Kind != "" && k.Kind != in.Kind {
			continue
		}
		kv := KindView{KindInfo: k, Models: []ModelView{}}
		def := aiservices.DefaultRef(k.Kind)
		for _, m := range aiservices.ModelsOf(k.Kind) {
			v := ModelView{Model: m, Default: m.Ref() == def, Retired: m.Retired(now), KeyReady: keyReady(a, m.Provider), Allowed: allows(rules, m.Provider)}
			if err := readyHere(a, m.Provider, k.Kind, m.ID); err != nil {
				v.KeyReady, v.NotHere = false, apperrMessage(err)
			}
			if k.Kind == aiservices.KindVideo {
				v.Built = true
				v.KeyReady = keyReady(a, m.Provider)
			} else {
				v.Built = built(m.Provider, k.Kind)
				v.PinnedFor = rules != nil && rules.Pin[k.Kind] == m.Ref()
				sample := aiservices.Request{Kind: k.Kind, Model: m.ID, Prompt: "x", Options: models.GenOptions{Seconds: 60}}
				sample.Inputs = []aiservices.Media{{Role: aiservices.RoleAudio, Seconds: 60}, {Role: aiservices.RoleMedia, Seconds: 60}}
				if e, err := aiservices.EstimateFor(m.Provider, &m, &sample, now); err == nil {
					v.Rate = e.Rate
				}
			}
			kv.Models = append(kv.Models, v)
			total++
		}
		out.Kinds = append(out.Kinds, kv)
	}
	for _, p := range aiservices.Providers() {
		out.Providers = append(out.Providers, ProviderView{Name: p.Name, Title: p.Title, Route: p.Route, KeyReady: keyReady(a, p.Name), Allowed: allows(rules, p.Name), Data: p.Data})
	}
	if in.Kind != "" && len(out.Kinds) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush has no kind of AI work called “%s”.", in.Kind)
	}
	out.Summary = fmt.Sprintf("%s across %s.", count(total, "model", "models"), count(len(out.Kinds), "kind of work", "kinds of work"))
	if !rules.Empty() {
		out.Summary += " This project's Content Settings: " + lowerFirst(rulesWords(rules)) + "."
	}
	return out, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// First-party abilities (D269): each kind of AI work as the menu and find
// show it, needing a key of any service the catalogue (aiservices) has a
// model of that kind at — so a new kind or service shows up by itself.
func init() {
	kind := func(id, k, title, does string, words ...string) ops.Offering {
		return ops.Offering{ID: id, Group: "make", Title: title, Does: does, Input: map[string]any{"kind": k}, Words: words,
			Needs: ops.Needs{Keys: keysFor(k), Priced: true, Leaves: leavesFor[k]}}
	}
	// A model on this computer: free, no key; works here once it is set up
	// (ai.local.setup, owners) — a check of its own (ready).
	here := func(id, k, model, title, does string, words ...string) ops.Offering {
		check := "local-" + model
		ops.RegisterReady(check, func(_ context.Context, a *app.App) (bool, string) {
			return readyHere(a, local.Name, k, model) == nil, localNeeds[model]
		})
		n := ops.Needs{Setup: check}
		if k == aiservices.KindBackground {
			n.OS = []string{"mac"}
		}
		return ops.Offering{ID: id, Group: "make", Title: title, Does: does, Input: map[string]any{"kind": k, "model": local.Name + "/" + model}, Words: words, Needs: n}
	}
	ops.Offer("ai.make",
		kind("ai-pictures", aiservices.KindPicture, "AI pictures", "Makes a picture from a description, or changes one of yours, with an AI service; priced first.", "image generation", "illustration", "thumbnail art"),
		kind("ai-speech", aiservices.KindVoice, "AI speech", "Says any words in an AI voice as a new sound clip; priced first.", "read this aloud", "spoken line"),
		kind("other-voice", aiservices.KindVoiceChange, "Another voice", "Changes the voice in a recording to another voice, keeping the words and their timing; priced first.", "voice changer", "disguise a voice"),
		kind("ai-sound-effects", aiservices.KindSoundEffect, "AI sound effects", "Makes a sound effect from a description; priced first.", "sfx", "whoosh", "foley"),
		kind("ai-music", aiservices.KindMusic, "AI music", "Makes music from a description; priced first.", "compose a song", "jingle"),
		kind("dubbing", aiservices.KindDubbing, "Dubbing", "Dubs a clip into another language in its speakers' voices, with their consent; priced first.", "translate the audio", "foreign language version"),
		kind("cloud-transcripts", aiservices.KindTranscription, "Cloud transcripts", "Writes down a clip's words with an AI service instead of on this computer, only when you ask; priced first.", "transcribe online"),
		kind("presenter-clips", aiservices.KindPresenter, "Presenter clips", "A presenter reads a clip's sound or a narration line on camera: a person's own presenter, with their consent, or a stock presenter licensed for client work; priced first.",
			"avatar", "talking head", "spokesperson video"),
		kind("lip-sync", aiservices.KindLipSync, "Lip sync", "Matches the lips in part of a clip to new sound, with the consent of everyone in it; priced first.", "change a spoken word", "match the mouth"),
		// The models on this computer (D278–D280): free, no key, nothing leaves.
		here("offline-voices", aiservices.KindVoice, local.ModelKokoro, "Offline voices", "Says any words in a free voice that runs on this computer; nothing leaves it.", "local text to speech", "free voice"),
		here("ai-larger-pictures", aiservices.KindUpscale, local.ModelUpscale, "AI larger pictures", "Makes a picture 2 or 4 times larger with an AI model on this computer; nothing leaves it.", "super resolution", "ai upscale"),
		here("ai-cut-outs", aiservices.KindBackground, local.ModelSubject, "AI cut-outs", "Takes a picture's background away with Apple Vision on this computer, as a new picture; nothing leaves it.", "remove the background", "subject lifting"))
	ops.Offer("presenter.make", ops.Offering{ID: "own-presenters", Group: "make", Title: "A person's own presenter",
		Does:  "Makes a presenter of a person at an AI service from their approved look, a training take and their consent filmed on camera, naming the service; people only, priced first.",
		Words: []string{"digital twin", "my own avatar"}, Needs: ops.Needs{Keys: keysFor(aiservices.KindPresenterMake), Priced: true,
			Leaves: "a video of the person and their filmed consent leave this computer, and the service keeps the presenter until it is retired"}})
	ops.Offer("ai.voices.copy", ops.Offering{ID: "voice-copies", Group: "make", Title: "Voice copies",
		Does:  "Copies a person's voice, with their recorded consent, so AI speech can be in their voice; priced first.",
		Words: []string{"clone a voice", "my own voice"}, Needs: ops.Needs{Keys: keysFor(aiservices.KindVoiceCopy), Priced: true,
			Leaves: "a recording of the person's voice leaves this computer"}})
}

// localNeeds says what a model on this computer needs, when it isn't set up.
var localNeeds = map[string]string{
	local.ModelKokoro:  "the Kokoro voice, set up once by an owner on this computer",
	local.ModelUpscale: "the Real-ESRGAN picture model, set up once by an owner on this computer",
	local.ModelSubject: "the on-screen helper, on a Mac",
}

// leavesFor says what leaves this computer for each kind of AI work.
var leavesFor = map[string]string{
	aiservices.KindLipSync:       "the part of the footage and the new sound leave this computer",
	aiservices.KindPicture:       "your words and any pictures you give it leave this computer",
	aiservices.KindVoice:         "the words leave this computer",
	aiservices.KindVoiceChange:   "the recording's sound leaves this computer",
	aiservices.KindSoundEffect:   "your words leave this computer",
	aiservices.KindMusic:         "your words leave this computer",
	aiservices.KindDubbing:       "the clip's sound leaves this computer",
	aiservices.KindTranscription: "the clip's sound leaves this computer",
	aiservices.KindPresenter:     "the sound it reads leaves this computer",
}

// keysFor are the integrations whose keys reach a model of a kind.
func keysFor(kind string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range aiservices.ModelsOf(kind) {
		p, err := aiservices.ProviderOf(m.Provider)
		if err != nil || p.Key == "" || seen[p.Key] {
			continue
		}
		seen[p.Key] = true
		out = append(out, p.Key)
	}
	return out
}
