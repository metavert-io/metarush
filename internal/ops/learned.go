package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// What a person's name was learned from, and taking a wrong one back (D158):
// the comparison dialogs ("Is this really Maya?", "Not Maya?") and
// `metarush people learned|unlearn` use these.

// PersonLearnedIn asks what a person's voice and face are learned from.
type PersonLearnedIn struct {
	ID string `json:"id" jsonschema:"description=Person ID or name (case and accents ignored; a unique start of the name is enough)."`
	// The voice in question (one being named as them): its clip and voice.
	AssetID string `json:"assetId,omitempty"`
	Speaker string `json:"speaker,omitempty" jsonschema:"description=The voice in question: its label\\, Speaker N or its name."`
	// FaceID is the face in question (named as them by its looks, or being named).
	FaceID string `json:"faceId,omitempty"`
}

// PersonLearnedOut lists what a person's voice and face are learned from.
type PersonLearnedOut struct {
	Person models.Person `json:"person"`
	// Voices are their confirmed voices, how alike each is to the others and to
	// the voice in question; the one least like the rest is marked odd.
	Voices []speakers.LearnedVoice `json:"voices"`
	// Faces are their confirmed faces (a group answer as one entry with its count).
	Faces []faces.LearnedFace `json:"faces"`
	// Voice and Face are the voice or face in question compared with them.
	Voice *speakers.VoiceInQuestion `json:"voice,omitempty"`
	Face  *faces.FaceInQuestion     `json:"face,omitempty"`
	// Still: the voice in question still sounds clearly unlike them (one much
	// lower: the warning holds); for a face in question, MetaRush still names
	// it after them by its looks.
	Still   bool   `json:"still"`
	Summary string `json:"summary"`
}

// PersonUnlearnIn takes back one thing a person's name was learned from.
type PersonUnlearnIn struct {
	ID string `json:"id" jsonschema:"description=Person ID or name."`
	// A learned voice to take back (its clip and voice)…
	AssetID string `json:"assetId,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	// …or a learned face (for a group answer: every face that answer named).
	FaceID string `json:"faceId,omitempty"`
	// Who it really is (Name matched like everywhere, OtherID by ID, NewPerson
	// adds Name as someone new); Unsure: nobody yet (it is asked about again).
	Name      string `json:"name,omitempty" jsonschema:"maxLength=80"`
	OtherID   string `json:"otherId,omitempty"`
	NewPerson bool   `json:"newPerson,omitempty"`
	Unsure    bool   `json:"unsure,omitempty"`
	// The voice or face in question, to say whether it is still unlike them.
	AgainstAssetID string `json:"againstAssetId,omitempty"`
	AgainstSpeaker string `json:"againstSpeaker,omitempty"`
	AgainstFaceID  string `json:"againstFaceId,omitempty"`
}

// PersonUnlearnOut is what a person's name is learned from after taking one back.
type PersonUnlearnOut struct {
	PersonLearnedOut
	Done string `json:"done"`
}

func learned(ctx context.Context, a *app.App, personID, assetID, speaker, faceID string) (*PersonLearnedOut, error) {
	p, err := resolvePerson(ctx, a, personID)
	if err != nil {
		return nil, err
	}
	vs, q, err := speakers.Learned(ctx, a, p.ID, assetID, speaker)
	if err != nil {
		return nil, speakerErr(err)
	}
	fs, fq, err := faces.LearnedFaces(ctx, a, p.ID, faceID)
	if err != nil {
		return nil, faceErr(err)
	}
	var np models.Person
	_ = a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), p.ID, &np)
	if np.ID == "" {
		np = p.Person
	}
	out := &PersonLearnedOut{Person: np, Voices: vs, Faces: fs, Voice: q, Face: fq}
	var b strings.Builder
	clips := map[string]bool{}
	for _, v := range vs {
		clips[v.AssetID] = true
	}
	switch len(vs) {
	case 0:
		fmt.Fprintf(&b, "Nobody has confirmed %s's voice in any clip yet.", np.Name)
	default:
		fmt.Fprintf(&b, "%s's voice is learned from %s.", np.Name, count(len(clips), "clip", "clips"))
	}
	for _, v := range vs {
		if v.Odd {
			fmt.Fprintf(&b, " One may be someone else: %s in “%s” (%s)", v.Speaker, v.AssetTitle, strings.TrimSuffix(strings.ToLower(v.Why[:1])+v.Why[1:], "."))
			b.WriteString(".")
		}
	}
	if len(fs) > 0 {
		n := 0
		for _, f := range fs {
			n += f.Faces
		}
		fmt.Fprintf(&b, " Their face is learned from %s.", count(n, "face you named", "faces you named"))
		for _, f := range fs {
			if f.Odd {
				fmt.Fprintf(&b, " One looks unlike the others (in “%s”).", f.AssetTitle)
			}
		}
	}
	switch {
	case q != nil && q.Differ:
		out.Still = true
		fmt.Fprintf(&b, " This voice still sounds quite different from %s's — one is much lower.", np.Name)
	case q != nil && len(vs) == 0:
		fmt.Fprintf(&b, " Nothing else is learned as %s's voice now: this one would be the first.", np.Name)
	case q != nil:
		fmt.Fprintf(&b, " This voice sounds like %s's others now.", np.Name)
	case fq != nil:
		for _, f := range fs {
			if f.Closest && f.LikeThis != nil {
				fmt.Fprintf(&b, " This face looks most like the one in “%s” (%d%% alike).", f.AssetTitle, pctOf(*f.LikeThis))
			}
		}
		if fq.Recognized {
			out.Still = true
			fmt.Fprintf(&b, " MetaRush names it %s by its looks.", np.Name)
		} else {
			fmt.Fprintf(&b, " MetaRush doesn't name it %s by its looks.", np.Name)
		}
	}
	out.Summary = strings.TrimSpace(b.String())
	return out, nil
}

func unlearn(ctx context.Context, a *app.App, in *PersonUnlearnIn) (*PersonUnlearnOut, error) {
	p, err := resolvePerson(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	hasOther := strings.TrimSpace(in.Name) != "" || in.OtherID != ""
	if !hasOther && !in.Unsure {
		return nil, invalid("Say who it really is, or that you're not sure.")
	}
	var done string
	switch {
	case in.FaceID != "":
		var other *models.Person
		if hasOther {
			op, _, err := speakers.PersonFor(ctx, a, in.Name, in.OtherID, in.NewPerson)
			if err != nil {
				return nil, speakerErr(err)
			}
			if op.ID == p.ID {
				return nil, invalid("Choose someone other than %s.", p.Name)
			}
			other = op
		}
		n, err := faces.TakeBack(ctx, a, p.ID, in.FaceID, other)
		if err != nil {
			if errors.Is(err, faces.ErrNotFound) {
				return nil, apperr.New(apperr.NotFound, "%s's face isn't learned from that face.", p.Name).WithHint("The list shows the faces it is learned from.").
					WithCommand(`metarush people learned "` + p.Name + `"`)
			}
			return nil, faceErr(err)
		}
		what := "That face isn't"
		if n > 1 {
			what = fmt.Sprintf("Those %d faces aren't", n)
		}
		done = fmt.Sprintf("%s %s any more; MetaRush learned %s's face again.", what, p.Name, p.Name)
		if other != nil {
			done = fmt.Sprintf("%s %s: they're %s now. MetaRush learned %s's face again.", what, p.Name, other.Name, p.Name)
		}
	case in.AssetID != "" && in.Speaker != "":
		vs, err := speakers.Voices(ctx, a, in.AssetID, false)
		if err != nil {
			return nil, err
		}
		v, err := speakers.ResolveLabel(vs, in.Speaker)
		if err != nil {
			return nil, speakerErr(err)
		}
		if v.PersonID != p.ID {
			return nil, apperr.New(apperr.NotFound, "%s's voice isn't learned from that voice.", p.Name).WithHint("The list shows the voices it is learned from.").
				WithCommand(`metarush people learned "` + p.Name + `"`)
		}
		if hasOther {
			np, err := speakers.SplitPerson(ctx, a, speakers.SplitInput{PersonID: p.ID, AssetID: in.AssetID, Label: v.Label, OtherID: in.OtherID, Name: in.Name, NewPerson: in.NewPerson})
			if err != nil {
				return nil, speakerErrFor(err, func(name, flag string) string {
					return `metarush people unlearn "` + p.Name + `" --asset ` + in.AssetID + ` --voice "` + in.Speaker + `" --as "` + name + `" ` + flag
				})
			}
			done = fmt.Sprintf("That voice is %s now, not %s; MetaRush learned %s's voice again.", np.Name, p.Name, p.Name)
		} else {
			lib, err := speakers.Reject(ctx, a, in.AssetID, v.Label, p.ID)
			if err != nil {
				return nil, speakerErr(err)
			}
			afterSpeakerChange(ctx, a, lib, p.ID)
			done = fmt.Sprintf("That voice isn't %s any more (we'll ask who it is); MetaRush learned %s's voice again.", p.Name, p.Name)
		}
		a.Audit(ctx, "people.unlearn", "person", p.ID, "", map[string]any{"assetId": in.AssetID, "label": v.Label})
	default:
		return nil, invalid("Say what to take back: a voice (its clip and voice) or a face.")
	}
	out, err := learned(ctx, a, p.ID, in.AgainstAssetID, in.AgainstSpeaker, in.AgainstFaceID)
	if err != nil {
		return nil, err
	}
	return &PersonUnlearnOut{PersonLearnedOut: *out, Done: done}, nil
}

func init() {
	Def("people.learned", "Show what a person's voice and face are learned from (the clips a person confirmed), how alike each is to the others and to a voice or face in question, and which one seems to be someone else", Flags{}, func(ctx context.Context, a *app.App, in *PersonLearnedIn) (*PersonLearnedOut, error) {
		return learned(ctx, a, in.ID, in.AssetID, in.Speaker, in.FaceID)
	})
	Def("people.unlearn", "Take back a voice or face a person's name was learned from (say who it really is, or that you're not sure): their voice and face are learned again at once", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *PersonUnlearnIn) (*PersonUnlearnOut, error) {
		return unlearn(ctx, a, in)
	})
}
