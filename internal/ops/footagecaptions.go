package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Captions files kept with a clip (D285): English captions an ability made
// for a clip spoken in another language (whisper.cpp's translation). They
// are the clip's, beside its transcript; a cut's own captions and their
// translations (D238) are unchanged.

// FootageCaptionsIn asks for the captions kept with a clip.
type FootageCaptionsIn struct {
	AssetID string `json:"assetId" jsonschema:"required"`
	// Language picks one file: its words come back too.
	Language string `json:"language,omitempty" jsonschema:"description=One language's captions (a code such as en): their words come back too."`
}

// FootageCaptionsFile is one captions file kept with a clip.
type FootageCaptionsFile struct {
	Language   string    `json:"language"`
	Name       string    `json:"name" jsonschema:"description=The language's name (English)."`
	Format     string    `json:"format" jsonschema:"enum=srt,enum=vtt"`
	Lines      int       `json:"lines"`
	Translated bool      `json:"translated" jsonschema:"description=A machine translated it."`
	Model      string    `json:"model,omitempty" jsonschema:"description=What made it."`
	MadeWith   string    `json:"madeWith,omitempty" jsonschema:"description=The ability that made it (its title)."`
	Ability    string    `json:"ability,omitempty" jsonschema:"description=name@version."`
	At         time.Time `json:"at"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
}

// FootageCaptionsOut lists them, and gives one file's words when asked.
type FootageCaptionsOut struct {
	AssetID string                `json:"assetId"`
	Title   string                `json:"title"`
	Files   []FootageCaptionsFile `json:"files"`
	// Text is the file asked for (language), as kept.
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Note   string `json:"note"`
}

func init() {
	Def("footage.captions", "List the captions files kept with a clip (English captions an ability made for a clip in another language, for instance), or give one language's file", Flags{},
		func(ctx context.Context, a *app.App, in *FootageCaptionsIn) (*FootageCaptionsOut, error) {
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
				return nil, notFound("asset", in.AssetID)
			}
			out := &FootageCaptionsOut{AssetID: as.ID, Title: as.DisplayTitle(), Files: []FootageCaptionsFile{}}
			var pick *models.AssetCaptions
			want := strings.ToLower(strings.TrimSpace(in.Language))
			for i := range as.Captions {
				c := &as.Captions[i]
				f := FootageCaptionsFile{Language: c.Language, Name: transcribe.SpokenLanguageName(c.Language), Format: c.Format, Lines: c.Lines, Translated: c.Translated, Model: c.Model,
					At: c.At, SHA256: c.SHA256, Size: c.Size}
				if m := c.MadeWith; m != nil {
					f.MadeWith, f.Ability = firstNonEmpty(m.Title, m.Name), m.Ability
				}
				out.Files = append(out.Files, f)
				if want != "" && strings.EqualFold(c.Language, want) && (pick == nil || c.At.After(pick.At)) {
					pick = c
				}
			}
			switch {
			case want != "" && pick == nil:
				return nil, apperr.New(apperr.NotFound, "“%s” has no captions in %s.", as.DisplayTitle(), firstNonEmpty(transcribe.SpokenLanguageName(want), want)).
					WithCommand("metarush footage captions " + as.ID)
			case pick != nil:
				p, err := a.Cache.Ensure(ctx, a.Media, pick.StorageKey, pick.SHA256, pick.Size, nil)
				if err != nil {
					return nil, err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return nil, err
				}
				out.Text, out.Format = string(b), pick.Format
				out.Note = fmt.Sprintf("The %s captions of “%s” (%d lines%s).", firstNonEmpty(transcribe.SpokenLanguageName(pick.Language), pick.Language), as.DisplayTitle(), pick.Lines,
					map[bool]string{true: ", machine-translated", false: ""}[pick.Translated])
			case len(out.Files) == 0:
				out.Note = fmt.Sprintf("“%s” has no captions files kept with it.", as.DisplayTitle())
			default:
				var names []string
				for _, f := range out.Files {
					names = append(names, firstNonEmpty(f.Name, f.Language))
				}
				out.Note = fmt.Sprintf("“%s” has captions in %s.", as.DisplayTitle(), strings.Join(names, ", "))
			}
			return out, nil
		})
}
