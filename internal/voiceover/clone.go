package voiceover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
)

// CloneIn creates an ElevenLabs voice from recordings of a person's voice.
// Cloning is for your own voice. Anyone else's voice needs that person's own
// recorded consent (an audio or video of them saying so), which is kept as an
// asset and named in the audit log.
type CloneIn struct {
	Name        string   `json:"name" jsonschema:"minLength=1,maxLength=80,description=Name for the new voice."`
	Samples     []string `json:"samples" jsonschema:"minItems=1,maxItems=25,description=Audio asset IDs with clean speech of the voice (one to a few minutes in total)."`
	Speaker     string   `json:"speaker,omitempty" jsonschema:"maxLength=120,description=Whose voice it is (default: you)."`
	OwnVoice    bool     `json:"ownVoice" jsonschema:"description=True when the samples are your own voice."`
	Consent     string   `json:"consent" jsonschema:"minLength=20,maxLength=2000,description=Consent statement in the speaker's words\\, e.g. 'I\\, Ada Lovelace\\, agree to MetaRush creating an AI copy of my voice for my own videos.'"`
	ConsentFrom string   `json:"consentRecording,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=Required for anyone else's voice: an audio/video asset of that person stating their consent."`
	Description string   `json:"description,omitempty" jsonschema:"maxLength=500"`
	RemoveNoise bool     `json:"removeBackgroundNoise,omitempty"`
	ProjectID   string   `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=Also use the new voice for this project."`
}

// CloneOut is the new voice.
type CloneOut struct {
	VoiceID              string `json:"voiceId"`
	Name                 string `json:"name"`
	RequiresVerification bool   `json:"requiresVerification"`
	UsedForProject       bool   `json:"usedForProject"`
	Summary              string `json:"summary"`
	JobID                string `json:"jobId,omitempty"`
}

func clone(ctx context.Context, a *app.App, in *CloneIn) (*CloneOut, error) {
	consent := strings.TrimSpace(in.Consent)
	speaker := strings.TrimSpace(in.Speaker)
	if speaker == "" {
		speaker = a.Actor.UserName
	}
	switch {
	case len([]rune(consent)) < 20:
		return nil, apperr.New(apperr.ConsentRequired, "A copy of a voice needs a consent statement in the speaker's own words.").
			WithHint(`For example: "I, ` + speaker + `, agree to an AI copy of my voice being made for my own videos."`)
	case !in.OwnVoice && in.ConsentFrom == "":
		return nil, apperr.New(apperr.ConsentRequired, "This is someone else's voice: MetaRush only copies it with their own recorded consent.").
			WithHint("Add a recording of " + speaker + " saying they agree to the project, then choose it as the consent recording.")
	case !in.OwnVoice && strings.EqualFold(speaker, a.Actor.UserName):
		return nil, apperr.New(apperr.InvalidInput, "Name the person whose voice this is.")
	}
	if !in.OwnVoice {
		var rec models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.ConsentFrom, &rec); err != nil || !rec.Media.HasAudio {
			return nil, apperr.New(apperr.ConsentRequired, "The consent recording must be a sound or video clip in this workspace.")
		}
	}
	client, err := providers.ElevenClientFor(ctx, a)
	if err != nil {
		return nil, err
	}
	if sub, err := client.Subscription(ctx); err == nil && !sub.CanUseInstantVoiceCloning {
		return nil, apperr.New(apperr.Provider, "Your ElevenLabs plan doesn't include copying a voice.")
	}
	var files []string
	whose := []string{in.ProjectID} // the projects whose voices go (What may leave, D290)
	for _, id := range in.Samples {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
			return nil, apperr.New(apperr.NotFound, "One of the voice samples isn't in this workspace.").WithDetails(map[string]any{"sample": id})
		}
		if !as.Media.HasAudio {
			return nil, apperr.New(apperr.UnsupportedMedia, "%s has no sound.", as.Name)
		}
		if len(as.ProjectIDs) == 0 {
			whose = append(whose, "")
		}
		whose = append(whose, as.ProjectIDs...)
		if err := leave.Check(ctx, a, leave.AI("elevenlabs", "ElevenLabs", []string{models.LeaveVoices}, whose, "voice samples for a voice copy")); err != nil {
			return nil, err
		}
		p, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
		if err != nil {
			return nil, err
		}
		// Give the upload a readable name with the right extension.
		tmp, done, err := scratchFile(a, "sample")
		if err != nil {
			return nil, err
		}
		defer done()
		named := filepath.Join(filepath.Dir(tmp), safeName(strings.TrimSuffix(as.Name, filepath.Ext(as.Name)))+strings.ToLower(filepath.Ext(as.Name)))
		if err := os.Link(p, named); err != nil {
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil, rerr
			}
			if err := os.WriteFile(named, b, 0o600); err != nil {
				return nil, err
			}
		}
		files = append(files, named)
	}
	desc := in.Description
	if desc == "" {
		desc = "Voice of " + speaker + " (made with MetaRush)"
	}
	if err := step(ctx, 0, 1, "Making your voice copy with ElevenLabs"); err != nil {
		return nil, err
	}
	voiceID, verify, err := client.CloneVoice(ctx, in.Name, desc, files, in.RemoveNoise)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "voiceover.clone", "voice", voiceID, in.ProjectID, map[string]any{"provider": providers.ElevenLabsName, "name": in.Name, "speaker": speaker,
		"ownVoice": in.OwnVoice, "consent": consent, "consentRecording": in.ConsentFrom, "samples": in.Samples, "aiVoice": true})
	out := &CloneOut{VoiceID: voiceID, Name: in.Name, RequiresVerification: verify}
	if in.ProjectID != "" {
		if _, err := providers.Get(providers.ElevenLabsName); err == nil {
			_, err := changeProjectVoice(ctx, a, in.ProjectID, voiceID)
			out.UsedForProject = err == nil
		}
	}
	out.Summary = fmt.Sprintf("Made the voice “%s” from %s. Everything made with it is marked as an AI voice.", in.Name, plural(len(in.Samples), "sample"))
	if verify {
		out.Summary += " ElevenLabs asks you to confirm it in your ElevenLabs account before you use it."
	}
	return out, nil
}

func changeProjectVoice(ctx context.Context, a *app.App, projectID, voiceID string) (any, error) {
	in := providers.SettingsIn{Name: providers.ElevenLabsName, Scope: "project", ProjectID: projectID, Voice: voiceID}
	return opsCall[providers.SettingsOut](ctx, a, "provider.settings", in)
}
