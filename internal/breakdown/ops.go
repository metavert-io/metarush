package breakdown

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// WhatItDoesIn names a clip.
type WhatItDoesIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1,description=The clip: its ID or its name."`
}

// WhatClip is the clip a card is about.
type WhatClip struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Reference: someone else's video, kept to learn from (no cut plays it).
	Reference bool `json:"reference,omitempty"`
	// Link, Platform and Who say where it came from (a link import).
	Link     string `json:"link,omitempty"`
	Platform string `json:"platform,omitempty"`
	Who      string `json:"who,omitempty"`
	// StartFrom is a draft asked for from it and how that went (D401).
	StartFrom *models.StartFromVideo `json:"startFrom,omitempty"`
}

// WhatItDoesOut is a clip's card.
type WhatItDoesOut struct {
	Clip    WhatClip   `json:"clip"`
	Card    WhatItDoes `json:"card"`
	Message string     `json:"message"`
}

func init() {
	ops.Def("footage.whatItDoes", "Say what a video does, from what MetaRush found in it (no AI service): its length and shape, its sections with times (hook, intro, parts, call to action, end card), pacing (shots, words a minute), who speaks and how much, captions burned in, titles and words on screen, what plays under the voices, and how it ends", ops.Flags{}, whatItDoes)
	// The menu and find (D269).
	ops.Offer("footage.whatItDoes", ops.Offering{ID: "what-a-video-does", Group: "understand", Title: "What a video does",
		Does:  "Says what a video does: its hook, intro, parts, call to action and end card with times, its pacing, captions and music.",
		Words: []string{"break down a video", "how is this video made", "structure of a video", "video breakdown"},
		Needs: ops.Needs{Works: []string{"transcripts", "editing"}}})
}

func whatItDoes(ctx context.Context, a *app.App, in *WhatItDoesIn) (*WhatItDoesOut, error) {
	as, err := ops.ResolveFootage(ctx, a, in.AssetID, "")
	if err != nil {
		return nil, err
	}
	out, err := CardOf(ctx, a, as)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CardOf makes a clip's card.
func CardOf(ctx context.Context, a *app.App, as *models.Asset) (*WhatItDoesOut, error) {
	if as.Kind == "image" {
		return nil, apperr.New(apperr.InvalidInput, "“%s” is a picture, not a video.", as.DisplayTitle())
	}
	if as.State != models.AssetAvailable {
		return nil, apperr.New(apperr.Conflict, "“%s” isn't ready yet: MetaRush is still bringing it in.", as.DisplayTitle()).WithDetails(map[string]any{"reason": "not_ready"})
	}
	input, err := Load(ctx, a, as)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read what it found in “%s”.", as.DisplayTitle())
	}
	c := Build(input)
	out := &WhatItDoesOut{Clip: clipOf(as), Card: *c}
	switch {
	case !c.Ready:
		out.Message = fmt.Sprintf("What “%s” does so far (MetaRush is still looking at %s).", as.DisplayTitle(), joinParts(c.Waiting))
	default:
		out.Message = fmt.Sprintf("What “%s” does.", as.DisplayTitle())
	}
	return out, nil
}

func clipOf(as *models.Asset) WhatClip {
	c := WhatClip{ID: as.ID, Title: as.DisplayTitle(), Reference: as.IsReference(), StartFrom: as.StartFrom}
	if s := as.Source; s != nil && s.URL != "" && s.Generated == nil && s.Remotion == nil && s.MadeWith == nil {
		c.Link, c.Platform, c.Who = s.URL, s.Platform, s.Who()
	}
	return c
}
