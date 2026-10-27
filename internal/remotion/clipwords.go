package remotion

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A clip of a composition with other words and pictures (D168, D169, D209),
// for a video made again with new words or new art: the same version,
// composition, props, frames and sound as a clip already made, drawn with
// words and pictures given for this video only — the source's own settings,
// and the cuts that follow them, don't change. Pictures go the way
// remotion.use and remotion.render take them (`pictures`): checked like a
// change of the source's pictures, the clip's own (`ownPictures`), part of its
// render key.

// ClipWord is one word setting a clip's composition reads.
type ClipWord struct {
	Key string `json:"key"`
	// Code is the words as the code writes them; Text the words the clip shows.
	Code     string `json:"code"`
	Text     string `json:"text"`
	OnScreen bool   `json:"onScreen"`
}

// ClipPicture is one picture setting a clip's composition draws (D209).
type ClipPicture struct {
	Name string `json:"name" jsonschema:"description=Its name as a setting (HERO)."`
	// File is the project's own picture (public/hero.png).
	File string `json:"file,omitempty"`
	// Shown is the picture the clip draws in its place — its own (Own) or the
	// source's replacement — nil when it draws the project's own.
	Shown *models.RemotionPictureValue `json:"shown,omitempty"`
	Own   bool                         `json:"own,omitempty"`
}

// ClipSettings is what a clip made from a Remotion source was drawn from and
// the word and picture settings its composition reads.
type ClipSettings struct {
	SourceID      string     `json:"sourceId"`
	SourceName    string     `json:"sourceName"`
	SourceTitle   string     `json:"sourceTitle"`
	ProjectID     string     `json:"projectId"`
	Version       string     `json:"version"`
	VersionNumber int        `json:"versionNumber"`
	Approved      bool       `json:"approved"`
	Composition   string     `json:"composition"`
	Duration      float64    `json:"duration" jsonschema:"description=The composition's length (seconds)."`
	Words         []ClipWord `json:"words,omitempty"`
	// Pictures are the picture settings the composition draws; PicturesAreSettings
	// says the version's pictures were made settings at all (the one-time change
	// a person approves, `remotion pictures setup`).
	Pictures            []ClipPicture `json:"pictures,omitempty"`
	PicturesAreSettings bool          `json:"picturesAreSettings"`
}

// SettingsOfClip reads what a clip made from a Remotion source was drawn
// with (nil when the clip isn't a render of a composition).
func SettingsOfClip(ctx context.Context, a *app.App, assetID string) (*ClipSettings, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, err
	}
	if !as.Source.IsRemotion() || as.Source.Remotion.Kind != models.SourceRemotionRender {
		return nil, nil
	}
	rc := as.Source.Remotion
	s, err := getSource(ctx, a, rc.SourceID)
	if err != nil {
		return nil, err
	}
	v, err := getVersion(ctx, a, rc.Version)
	if err != nil {
		return nil, err
	}
	out := &ClipSettings{SourceID: s.ID, SourceName: s.Name, SourceTitle: s.Title, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number,
		Approved: v.State == models.RemotionApproved, Composition: rc.Composition, PicturesAreSettings: v.HasPictureSettings()}
	c := v.Composition(rc.Composition)
	if c != nil {
		out.Duration = c.Duration
	}
	shown := map[string]string{}
	if rc.Words != "" {
		_ = json.Unmarshal([]byte(rc.Words), &shown)
	}
	for _, st := range v.Settings {
		if !usesKey(c, st.Key) {
			continue
		}
		w := ClipWord{Key: st.Key, Code: st.Text, Text: st.Text, OnScreen: st.OnScreen}
		if t, ok := shown[st.Key]; ok {
			w.Text = t
		}
		out.Words = append(out.Words, w)
	}
	sort.Slice(out.Words, func(i, j int) bool { return out.Words[i].Key < out.Words[j].Key })
	// The pictures it draws now: the source's replacements, with its own over them.
	_, drawn, _ := picturesFor(s, v, rc.Composition, rc.OwnPictures)
	now := map[string]models.RemotionPictureValue{}
	for _, p := range drawn {
		now[p.Name] = p
	}
	own := map[string]bool{}
	for _, p := range rc.OwnPictures {
		own[p.Name] = p.SHA256 != ""
	}
	for _, p := range v.Pictures {
		if !p.Setting || !drawsPicture(c, p.Name) {
			continue
		}
		cp := ClipPicture{Name: p.Name, File: p.File, Own: own[p.Name]}
		if val, ok := now[p.Name]; ok {
			val := val
			cp.Shown = &val
		}
		out.Pictures = append(out.Pictures, cp)
	}
	sort.Slice(out.Pictures, func(i, j int) bool { return out.Pictures[i].Name < out.Pictures[j].Name })
	return out, nil
}

// ClipChange is what a clip is drawn with for one video: words by setting key
// (empty: the code's own words), and pictures of its own by name (a resource
// the source's project can use; no resource: the code's own picture).
type ClipChange struct {
	Words    map[string]string
	Pictures []RemotionPictureSet
}

// ClipWith asks for a clip like assetID (the same version, composition,
// props, frames and sound) drawn with these words and pictures: the keys and
// names its composition reads, the others as the clip shows them. Pictures
// are checked like remotion.use's (the version's pictures must be settings)
// and kept as the clip's own. It returns the render, done (its clip ready) or
// queued (its job), made once per version, composition, props, frames, words
// and pictures like every render — no new version of the code, no approval.
func ClipWith(ctx context.Context, a *app.App, assetID string, ch ClipChange) (*models.RemotionRender, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, err
	}
	if !as.Source.IsRemotion() || as.Source.Remotion.Kind != models.SourceRemotionRender {
		return nil, apperr.New(apperr.InvalidInput, "“%s” wasn't made from a Remotion project, so its words and pictures can't change.", as.DisplayTitle())
	}
	rc := as.Source.Remotion
	s, err := getSource(ctx, a, rc.SourceID)
	if err != nil {
		return nil, err
	}
	v, err := getVersion(ctx, a, rc.Version)
	if err != nil {
		return nil, err
	}
	c, err := requireRenderable(s, v, rc.Composition)
	if err != nil {
		return nil, err
	}
	have := settingByKey(v)
	use := map[string]string{}
	if rc.Words != "" {
		_ = json.Unmarshal([]byte(rc.Words), &use)
	}
	for k, t := range ch.Words {
		st, ok := have[k]
		if !ok || !usesKey(c, k) {
			return nil, apperr.New(apperr.InvalidInput, "“%s” doesn't show words called %s.", c.ID, k)
		}
		if len([]rune(t)) > maxWordLength {
			return nil, apperr.New(apperr.InvalidInput, "The new words for %s are too long (more than %d characters).", k, maxWordLength)
		}
		if t == "" || t == st.Text {
			delete(use, k) // the code's own words
			continue
		}
		use[k] = t
	}
	wordsJSON := ""
	if len(use) > 0 {
		b, _ := json.Marshal(use) // map keys are sorted: canonical
		wordsJSON = string(b)
	}
	// The clip's own pictures: what it had, with these over them.
	own := append([]models.RemotionPictureValue(nil), rc.OwnPictures...)
	if len(ch.Pictures) > 0 {
		for _, p := range ch.Pictures {
			if !drawsPicture(c, p.Name) {
				return nil, apperr.New(apperr.InvalidInput, "“%s” doesn't draw a picture called %s.", c.ID, p.Name)
			}
		}
		vals, err := ownPictures(ctx, a, s, v, ch.Pictures)
		if err != nil {
			return nil, err
		}
		byName := map[string]models.RemotionPictureValue{}
		for _, p := range own {
			byName[p.Name] = p
		}
		for _, p := range vals {
			byName[p.Name] = p
		}
		own = sortedValues(byName)
	}
	pics, pvals, pn := picturesFor(s, v, c.ID, own)
	return requestRender(ctx, a, renderRequest{Source: s, Version: v, Composition: c.ID, Props: rc.Props, Frames: rc.Frames, Sound: rc.Sound,
		Words: wordsJSON, Pictures: pics, PictureValues: pvals, PicturesNumber: pn, OwnPictures: own, KeepSettings: true})
}

// RenderState reads a render asked for with ClipWith by its key.
func RenderState(ctx context.Context, a *app.App, key string) (*models.RemotionRender, error) {
	return getRender(ctx, a, key)
}
