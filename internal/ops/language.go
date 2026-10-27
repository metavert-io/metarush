package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// FootageLanguageIn asks which language a clip is spoken in.
type FootageLanguageIn struct {
	AssetID string `json:"assetId" jsonschema:"required"`
	// Choices also lists the languages the clip can be set to.
	Choices bool `json:"choices,omitempty"`
}

// FootageLanguageOut is a clip's language (D243): what it is, how MetaRush
// knows, and what wrote its words down.
type FootageLanguageOut struct {
	AssetID  string                 `json:"assetId"`
	Title    string                 `json:"title"`
	Language *models.SpokenLanguage `json:"language,omitempty"`
	// Name is the language's name ("Spanish").
	Name string `json:"name,omitempty"`
	// HeardName is another language the detection heard, not surely enough
	// to write the clip down in it.
	HeardName string `json:"heardName,omitempty"`
	// Sure is false when a person may want to check it: the detection wasn't
	// sure, or heard another language.
	Sure bool `json:"sure"`
	// WordsIn is the language the clip's words are written in now, when it
	// differs from Language (they are being written down again).
	WordsIn    string `json:"wordsIn,omitempty"`
	Recognizer string `json:"recognizer,omitempty"`
	Note       string `json:"note"`
	// Choices are the languages it can be set to (with choices).
	Choices []transcribe.LanguageChoice `json:"choices,omitempty"`
}

// FootageLanguageSetIn says which language a clip is spoken in.
type FootageLanguageSetIn struct {
	AssetID string `json:"assetId" jsonschema:"required"`
	// Language is a code (es) or a name (Spanish).
	Language string `json:"language" jsonschema:"required"`
}

// FootageLanguageSetOut is the language set and the transcription started.
type FootageLanguageSetOut struct {
	AssetID  string `json:"assetId"`
	Language string `json:"language"`
	Name     string `json:"name"`
	// JobID is the transcription that writes the words down again ("" when
	// the clip already had this language and its words).
	JobID string `json:"jobId,omitempty"`
	Note  string `json:"note"`
}

func init() {
	Def("footage.language", "Say which language a clip is spoken in, how MetaRush knows (heard, set by a person, or the workspace's) and what wrote its words down", Flags{}, func(ctx context.Context, a *app.App, in *FootageLanguageIn) (*FootageLanguageOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		out := LanguageOf(&as)
		if in.Choices {
			out.Choices = transcribe.SpokenLanguages()
		}
		return out, nil
	})
	Def("footage.language.set", "Set the language a clip is spoken in and write its words down again in that language (English with the English recognizer, other languages with whisper.cpp)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *FootageLanguageSetIn) (*FootageLanguageSetOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		if !as.Media.HasAudio {
			return nil, invalid("“%s” has no sound, so nothing in it is spoken.", as.DisplayTitle())
		}
		code, ok := SpokenLanguageCode(in.Language)
		if !ok {
			return nil, invalid("MetaRush doesn't know the language “%s”. Say it by name (Spanish) or by its code (es).", strings.TrimSpace(in.Language))
		}
		name := transcribe.SpokenLanguageName(code)
		out := &FootageLanguageSetOut{AssetID: as.ID, Language: code, Name: name}
		if l := as.Language; l != nil && l.By == models.LanguagePerson && l.Code == code && as.Transcript != nil && as.Transcript.State == "available" && sameLanguage(as.Transcript.Language, code) {
			out.Note = fmt.Sprintf("“%s” is already set to %s, and its words are written down in %s.", as.DisplayTitle(), name, name)
			return out, nil
		}
		now := time.Now().UTC()
		set := models.SpokenLanguage{Code: code, By: models.LanguagePerson, UserID: a.Actor.UserID, At: now}
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bsonSet("language", set)); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s:%s:language:%s:%s", jobs.TypeTranscribe, as.ID, code, strings.ReplaceAll(nowStamp(), ":", ""))
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeTranscribe, IdempotencyKey: key, Priority: 9, Heavy: true, AssetID: as.ID,
			Payload: map[string]any{"assetId": as.ID, "language": code, "languageBy": models.LanguagePerson, "userId": a.Actor.UserID, "reason": "language"}, Actor: a.Actor})
		if err != nil {
			return nil, err
		}
		before := ""
		if l := as.Language; l != nil {
			before = l.Code
		} else if as.Transcript != nil {
			before = as.Transcript.Language
		}
		a.Audit(ctx, "footage.language.set", "asset", as.ID, "", map[string]any{"language": code, "before": before})
		out.JobID = j.ID
		out.Note = fmt.Sprintf("Writing down the words of “%s” again in %s. Fixes people made carry over where the same words are heard again.", as.DisplayTitle(), name)
		return out, nil
	})
}

// LanguageOf says a clip's language in plain words (footage.language; the
// info card and the clip's transcript use it too).
func LanguageOf(as *models.Asset) *FootageLanguageOut {
	out := &FootageLanguageOut{AssetID: as.ID, Title: as.DisplayTitle(), Language: as.Language, Sure: true}
	title := as.DisplayTitle()
	if as.Transcript != nil && as.Transcript.Provider != "" {
		out.Recognizer = as.Transcript.Provider
		if as.Transcript.Model != "" {
			out.Recognizer += " " + as.Transcript.Model
		}
	}
	l := as.Language
	if l == nil || l.Code == "" {
		switch {
		case !as.Media.HasAudio:
			out.Note = fmt.Sprintf("“%s” has no sound.", title)
		case as.Transcript != nil && as.Transcript.State == "available" && as.Transcript.Language != "":
			out.Name = transcribe.SpokenLanguageName(as.Transcript.Language)
			out.Note = fmt.Sprintf("The words of “%s” are written down in %s. Its language wasn't listened for (it was transcribed before MetaRush did); set it if that's wrong.", title, out.Name)
		default:
			out.Note = fmt.Sprintf("MetaRush listens for the language of “%s” when it writes its words down.", title)
		}
		return out
	}
	out.Name = transcribe.SpokenLanguageName(l.Code)
	if as.Transcript != nil && as.Transcript.Language != "" && !sameLanguage(as.Transcript.Language, l.Code) {
		out.WordsIn = transcribe.SpokenLanguageName(as.Transcript.Language)
	}
	switch l.By {
	case models.LanguagePerson:
		out.Note = fmt.Sprintf("“%s” is spoken in %s (a person set it).", title, out.Name)
	case models.LanguageHeard:
		out.Note = fmt.Sprintf("MetaRush heard “%s” in %s.", title, out.Name)
	default:
		if l.Heard != "" {
			out.HeardName, out.Sure = transcribe.SpokenLanguageName(l.Heard), false
			out.Note = fmt.Sprintf("MetaRush wasn't sure about “%s”: it sounded like %s, so its words are written down in %s, this workspace's language. If it is %s, set it and MetaRush writes it down again.",
				title, out.HeardName, out.Name, out.HeardName)
		} else {
			out.Note = fmt.Sprintf("“%s” is written down in %s, this workspace's language.", title, out.Name)
		}
	}
	if out.WordsIn != "" {
		out.Note += fmt.Sprintf(" Its words are still in %s until they are written down again.", out.WordsIn)
	}
	return out
}

// SpokenLanguageCode reads a language people typed (es, ES, es-419, Spanish)
// as the code whisper.cpp uses (es); ok is false when it knows no such language.
func SpokenLanguageCode(s string) (string, bool) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(strings.ReplaceAll(s, "_", "-"))
	if i := strings.IndexByte(low, '-'); i > 0 {
		low = low[:i]
	}
	if transcribe.KnownLanguage(low) {
		return low, true
	}
	for _, l := range transcribe.SpokenLanguages() {
		if strings.EqualFold(l.Name, s) {
			return l.Code, true
		}
	}
	return "", false
}

func sameLanguage(a, b string) bool {
	x, _ := SpokenLanguageCode(a)
	y, _ := SpokenLanguageCode(b)
	return x != "" && x == y
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.language", Offering{ID: "spoken-language", Group: "understand", Title: "Each clip's language",
		Does:  "Says which language each clip is spoken in, and writes its words down in that language.",
		Words: []string{"detect language", "spanish", "french", "another language"}, Needs: Needs{Works: []string{"transcripts", "editing"}, Programs: []string{"whisper-cli"}}})
}
