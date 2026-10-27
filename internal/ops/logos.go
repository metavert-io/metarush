package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Logo files (D198): the brand of a project's Content Settings has five
// named logos — Main, For dark backgrounds, For light backgrounds, Square icon
// and Wordmark — each a picture in the resource library (versioned,
// content-addressed; SVGs cleaned, a PDF's first page drawn), visible to
// every project using the settings. New cuts pin them (logo, logo-dark,
// logo-light, logo-icon, logo-wordmark), and graphics showing the main logo
// show the one made for their background. The dashboard uploads the file
// first (the ordinary upload pipeline); `metarush content logo set <which>
// <file>` adds a local file the same way, then names it.

// ContentLogoSetIn makes a picture one of the brand's named logos.
type ContentLogoSetIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's\\, what new projects use."`
	Which     string `json:"which" jsonschema:"enum=main,enum=dark,enum=light,enum=icon,enum=wordmark,description=Which logo: main\\, dark (for dark backgrounds)\\, light (for light backgrounds)\\, icon (square) or wordmark."`
	Resource  string `json:"resource" jsonschema:"minLength=1,description=The picture: a resource's short name\\, title or ID (add a file first; the CLI does both)."`
}

// ContentLogoRemoveIn takes one of the brand's named logos away.
type ContentLogoRemoveIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's."`
	Which     string `json:"which" jsonschema:"enum=main,enum=dark,enum=light,enum=icon,enum=wordmark"`
}

// LogoWhich reads a logo's name as people write it: main, dark, light, icon
// (square), wordmark ("" when it isn't one).
func LogoWhich(s string) string {
	switch strings.ToLower(strings.TrimSpace(strings.NewReplacer("_", "-", " ", "-").Replace(s))) {
	case "main", "logo", "primary":
		return models.LogoMain
	case "dark", "for-dark", "for-dark-backgrounds", "logo-dark", "on-dark":
		return models.LogoDark
	case "light", "for-light", "for-light-backgrounds", "logo-light", "on-light":
		return models.LogoLight
	case "icon", "square", "square-icon", "logo-icon":
		return models.LogoIcon
	case "wordmark", "word-mark", "logo-wordmark", "name":
		return models.LogoWordmark
	}
	return ""
}

func logoWhichErr(which string) error {
	return apperr.New(apperr.InvalidInput, "There's no logo called %q.", which).
		WithHint("Say main, dark (for dark backgrounds), light (for light backgrounds), icon (square) or wordmark.")
}

// logoSetIn is the brand.set that changes one named logo.
func logoSetIn(projectID, which, ref string) *BrandSetIn {
	in := &BrandSetIn{ProjectID: projectID}
	switch which {
	case models.LogoMain:
		in.Logo = &ref
	case models.LogoDark:
		in.LogoDark = &ref
	case models.LogoLight:
		in.LogoLight = &ref
	case models.LogoIcon:
		in.LogoIcon = &ref
	case models.LogoWordmark:
		in.LogoWordmark = &ref
	}
	return in
}

func init() {
	Def("content.logo.set", "Make a picture one of the brand's named logos in a project's own Content Settings (without a project, the default's): main, for dark backgrounds, for light backgrounds, a square icon or the wordmark; every project using the settings gets it, and graphics showing the main logo show the one made for their background", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ContentLogoSetIn) (*BrandShowOut, error) {
			which := LogoWhich(in.Which)
			if which == "" {
				return nil, logoWhichErr(in.Which)
			}
			ref := strings.TrimSpace(in.Resource)
			if ref == "" {
				return nil, invalid("Say which picture: its name in the resources, or add the file first.")
			}
			// A logo is a picture (brand.set refuses fonts, repositories and web page snapshots).
			out, err := brandSet(ctx, a, logoSetIn(in.ProjectID, which, ref))
			if err != nil {
				return nil, err
			}
			var title string
			for _, l := range out.Logos {
				if l.Which == which && l.Resource != nil {
					title = l.Resource.Title
				}
			}
			whose := "the brand"
			if out.From != nil {
				whose = q(out.From.Name) + "'s brand"
			}
			msg := fmt.Sprintf("%s is the %s of %s now.", q(title), logoWords(which), whose)
			if which == models.LogoMain && strings.Contains(out.Message, "Colours taken from the logo") {
				msg += " Colours taken from it; change any of them and MetaRush keeps yours."
			}
			out.Message = strings.TrimSpace(msg + " " + afterFirstSentence(out.Message))
			return out, nil
		})
	Def("content.logo.remove", "Take one of the brand's named logos out of a project's own Content Settings (without a project, the default's); the picture stays in the resources and saved versions keep it", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ContentLogoRemoveIn) (*BrandShowOut, error) {
			which := LogoWhich(in.Which)
			if which == "" {
				return nil, logoWhichErr(in.Which)
			}
			out, err := brandSet(ctx, a, logoSetIn(in.ProjectID, which, ""))
			if err != nil {
				return nil, err
			}
			whose := "the brand"
			if out.From != nil {
				whose = q(out.From.Name) + "'s brand"
			}
			out.Message = strings.TrimSpace(fmt.Sprintf("%s has no %s now; the picture stays in the resources.", upperFirst(whose), logoWords(which)) + " " + afterFirstSentence(out.Message))
			return out, nil
		})
}

// logoWords names a logo inside a sentence ("logo for dark backgrounds").
func logoWords(which string) string {
	switch which {
	case models.LogoMain:
		return "main logo"
	case models.LogoDark:
		return "logo for dark backgrounds"
	case models.LogoLight:
		return "logo for light backgrounds"
	case models.LogoIcon:
		return "square icon"
	case models.LogoWordmark:
		return "wordmark"
	}
	return "logo"
}

// afterFirstSentence keeps what brand.set's message says after its first
// sentence (who else uses the settings, the latest cuts' offer).
func afterFirstSentence(s string) string {
	for _, keep := range []string{"These are ", "Also used by ", "Saved versions don't change"} {
		if i := strings.Index(s, keep); i >= 0 {
			return s[i:]
		}
	}
	return ""
}
