package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// On-screen words as settings (D168, D169). Once a person approved the
// one-time change that wraps a project's copy files in words("NAME", …), its
// words are settings: changing them makes new renders of the compositions
// that read them (passed as the input prop metarushWords), checked in the
// render jail for the length they produce and words that no longer fit, and
// the latest cuts' clips move to them — no new version of the code, no new
// approval.

// maxWordLength is the longest a setting's words may be.
const maxWordLength = 500

// usesKey reports whether a composition reads a setting: one of the paths its
// code reads is the key or a part of it (CONNECT.sources reads
// CONNECT.sources.3.name).
func usesKey(c *models.RemotionComposition, key string) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Words {
		if p == key || strings.HasPrefix(key, p+".") {
			return true
		}
	}
	return false
}

// settingByKey indexes a version's settings.
func settingByKey(v *models.RemotionVersion) map[string]models.RemotionWordSetting {
	out := map[string]models.RemotionWordSetting{}
	for _, s := range v.Settings {
		out[s.Key] = s
	}
	return out
}

// wordValues are the settings of a source that apply to a version: changed
// words whose keys the version has.
func wordValues(s *models.RemotionSource, v *models.RemotionVersion) map[string]string {
	out := map[string]string{}
	if s.Words == nil || len(v.Settings) == 0 {
		return out
	}
	have := settingByKey(v)
	for _, w := range s.Words.Values {
		if st, ok := have[w.Key]; ok && st.Text != w.Text {
			out[w.Key] = w.Text
		}
	}
	return out
}

// wordsFor is what a render of a composition passes as word settings: the
// changed words it reads, as canonical JSON ("" when none), and the change
// they come from.
func wordsFor(s *models.RemotionSource, v *models.RemotionVersion, comp string) (string, int) {
	vals := wordValues(s, v)
	if len(vals) == 0 {
		return "", 0
	}
	c := v.Composition(comp)
	use := map[string]string{}
	for k, t := range vals {
		if usesKey(c, k) {
			use[k] = t
		}
	}
	if len(use) == 0 {
		return "", 0
	}
	b, _ := json.Marshal(use) // map keys are sorted: canonical
	return string(b), s.Words.Number
}

// renderProps are a render's input props: its own props, its word settings
// under metarushWords (what src/metarush-words.ts reads) and the pictures in
// place of the project's own under metarushPictures (src/metarush-pictures.ts,
// D209).
func renderProps(props, words string, pics []models.RemotionPictureValue) map[string]any {
	var out map[string]any
	if props != "" {
		_ = json.Unmarshal([]byte(props), &out)
	}
	if words != "" {
		var w map[string]any
		if json.Unmarshal([]byte(words), &w) == nil && len(w) > 0 {
			if out == nil {
				out = map[string]any{}
			}
			out["metarushWords"] = w
		}
	}
	if p := picturesProp(pics); p != nil {
		if out == nil {
			out = map[string]any{}
		}
		out["metarushPictures"] = p
	}
	return out
}

// ---- ops ----

// RemotionWordsIn picks a source (its approved version, else its newest).
type RemotionWordsIn struct {
	Source    string `json:"source" jsonschema:"minLength=1,description=The source's short name (name@version for another version)."`
	ProjectID string `json:"projectId,omitempty"`
}

// RemotionWordRow is one setting.
type RemotionWordRow struct {
	Key      string   `json:"key"`
	Code     string   `json:"code" jsonschema:"description=The words as the code writes them."`
	Text     string   `json:"text" jsonschema:"description=The words now (the code's\\, or as changed)."`
	Changed  bool     `json:"changed"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Name     string   `json:"name"`
	OnScreen bool     `json:"onScreen" jsonschema:"description=It reads like words (not a value the code uses)."`
	Seen     []string `json:"seen" jsonschema:"description=Where the map saw it on screen (composition · scene)."`
	Used     []string `json:"used" jsonschema:"description=Compositions whose code reads it."`
}

// RemotionWordsOut is a source's words: its settings, or where its words are.
type RemotionWordsOut struct {
	Name          string                       `json:"name"`
	Title         string                       `json:"title"`
	Version       string                       `json:"version"`
	VersionNumber int                          `json:"versionNumber"`
	HasSettings   bool                         `json:"hasSettings" jsonschema:"description=Its words are settings (changed without a new version)."`
	Settings      []RemotionWordRow            `json:"settings"`
	Copy          []models.RemotionCopyFile    `json:"copy" jsonschema:"description=The files that hold its words."`
	Number        int                          `json:"number" jsonschema:"description=How many times its words were changed."`
	Changes       []models.RemotionWordsChange `json:"changes"`
	Waiting       *RemotionVersionRow          `json:"waiting,omitempty" jsonschema:"description=A version that makes its words settings\\, waiting for a person."`
	// Pending is the last change of words that was only checked; Last the
	// last change made — each with its before/after pictures.
	Pending *RemotionWordsCheckOut `json:"pending,omitempty"`
	Last    *RemotionWordsCheckOut `json:"last,omitempty"`
	Message string                 `json:"message"`
	Next    string                 `json:"next,omitempty"`
	URL     string                 `json:"url"`
}

// RemotionWordsCheckOut is a change of words and what drawing it found, with
// its before/after pictures.
type RemotionWordsCheckOut struct {
	models.RemotionWordsChange
	Stills []RemotionStillRow `json:"stills"`
	Made   bool               `json:"made" jsonschema:"description=It was made (not only checked)."`
}

// wordsCheckOut adds the picture addresses to a change (change 0: the pending one).
func wordsCheckOut(c *models.RemotionWordsChange, made bool) *RemotionWordsCheckOut {
	if c == nil {
		return nil
	}
	out := &RemotionWordsCheckOut{RemotionWordsChange: *c, Stills: []RemotionStillRow{}, Made: made}
	n := c.Number
	if !made {
		n = 0
	}
	if c.Check != nil && c.Check.Version != "" {
		for i, st := range c.Check.Stills {
			r := RemotionStillRow{RemotionStill: st, AfterURL: WordsStillURL(c.Check.Version, n, i, "after")}
			if st.Before != nil {
				r.BeforeURL = WordsStillURL(c.Check.Version, n, i, "before")
			}
			out.Stills = append(out.Stills, r)
		}
	}
	return out
}

// WordsStillURL is where the dashboard shows a still of a change of words
// (change 0: the last one that was only checked).
func WordsStillURL(version string, change, i int, side string) string {
	return fmt.Sprintf("/media/remotion/%s/words/%d-%d/%s", version, change, i, side)
}

// WordsStill finds a still of a change of words (the media server).
func WordsStill(ctx context.Context, a *app.App, v *models.RemotionVersion, change, i int, side string) *models.RemotionObject {
	s, err := getSource(ctx, a, v.SourceID)
	if err != nil {
		return nil
	}
	var c *models.RemotionWordsChange
	if change == 0 {
		c = s.WordsPending
	} else if s.Words != nil {
		for k := range s.Words.Changes {
			if s.Words.Changes[k].Number == change {
				c = &s.Words.Changes[k]
			}
		}
	}
	if c == nil || c.Check == nil || c.Check.Version != v.Hash || i < 0 || i >= len(c.Check.Stills) {
		return nil
	}
	st := &c.Check.Stills[i]
	if side == "before" {
		return st.Before
	}
	return &st.After
}

// RemotionWordsSetupIn makes the version that turns a source's words into settings.
type RemotionWordsSetupIn struct {
	Source    string   `json:"source" jsonschema:"minLength=1"`
	ProjectID string   `json:"projectId,omitempty"`
	Files     []string `json:"files,omitempty" jsonschema:"maxItems=20,description=Only these copy files (default: every file that holds its words)."`
}

// RemotionWordsSetIn changes words.
type RemotionWordsSetIn struct {
	Source    string                     `json:"source" jsonschema:"minLength=1"`
	ProjectID string                     `json:"projectId,omitempty"`
	Set       []models.RemotionWordValue `json:"set" jsonschema:"minItems=1,maxItems=200,description=Settings and their new words (empty words: back to the code's)."`
	Note      string                     `json:"note,omitempty" jsonschema:"maxLength=2000"`
	CheckOnly bool                       `json:"checkOnly,omitempty" jsonschema:"description=Draw the change and check it\\, without making it."`
}

// RemotionTaskOut is work queued on a source.
type RemotionTaskOut struct {
	Message string `json:"message"`
	JobID   string `json:"jobId"`
	State   string `json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed"`
	Name    string `json:"name"`
}

func init() {
	ops.Def("remotion.words", "Show a Remotion source's on-screen words: its settings (the words as the code writes them and as changed, where they are written and where they are seen), or the files that hold its words when they aren't settings yet", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionWordsIn) (*RemotionWordsOut, error) {
			return wordsOp(ctx, a, in)
		})
	ops.Def("remotion.words.setup", "Make a Remotion source's on-screen words settings: the one-time change to its code (its copy files wrapped so words come from settings) as the next version, checked in the render jail; a person approves it", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionWordsSetupIn) (*RemotionTaskOut, error) {
			return wordsSetupOp(ctx, a, in)
		})
	ops.Def("remotion.words.set", "Change on-screen words of a Remotion source whose words are settings: drawn in the render jail with the new words (lengths, words that no longer fit, before/after stills), then the latest cuts' clips are made again with them — no new version of the code", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionWordsSetIn) (*RemotionTaskOut, error) {
			return wordsSetOp(ctx, a, in)
		})
}

// nearestMap is the version's map, else the newest map of an earlier version
// (where words are seen rarely moves between versions).
func nearestMap(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) (*models.RemotionMap, error) {
	if doc, err := loadMap(ctx, a, v); err == nil {
		return doc, nil
	}
	vs, err := versionsOf(ctx, a, s.ID)
	if err != nil {
		return nil, err
	}
	var best *models.RemotionVersion
	for i := range vs {
		x := &vs[i]
		if x.Number < v.Number && x.Map != nil && x.Map.State == models.RemotionMapDone && (best == nil || x.Number > best.Number) {
			best = x
		}
	}
	if best == nil {
		return nil, apperr.New(apperr.NotFound, "No version of “%s” has a map yet.", s.Title)
	}
	return loadMap(ctx, a, best)
}

// wordsVersion is the version whose settings count: the approved one (renders
// use it), else the newest.
func wordsVersion(ctx context.Context, a *app.App, projectRef, ref string) (*models.RemotionSource, *models.RemotionVersion, error) {
	pid, err := projectIDOf(ctx, a, projectRef)
	if err != nil {
		return nil, nil, err
	}
	name, ver := splitRef(ref)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, nil, err
	}
	if s.Head == "" {
		return nil, nil, apperr.New(apperr.Validation, "“%s” is still being brought in.", s.Title)
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, nil, err
	}
	return s, v, nil
}

func wordsOp(ctx context.Context, a *app.App, in *RemotionWordsIn) (*RemotionWordsOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	out := &RemotionWordsOut{Name: s.Name, Title: s.Title, Version: v.Hash, VersionNumber: v.Number, HasSettings: len(v.Settings) > 0, Settings: []RemotionWordRow{},
		Copy: v.Copy, Changes: []models.RemotionWordsChange{}, URL: links.Remotion(s.ProjectID, s.ID, v.Hash)}
	if out.Copy == nil {
		out.Copy = []models.RemotionCopyFile{}
	}
	if s.Words != nil {
		out.Number = s.Words.Number
		out.Changes = s.Words.Changes
		if len(out.Changes) > 10 {
			out.Changes = out.Changes[:10]
		}
		if len(s.Words.Changes) > 0 {
			out.Last = wordsCheckOut(&s.Words.Changes[0], true)
		}
	}
	out.Pending = wordsCheckOut(s.WordsPending, false)
	vals := map[string]string{}
	if s.Words != nil {
		for _, w := range s.Words.Values {
			vals[w.Key] = w.Text
		}
	}
	seen := map[string][]string{}
	if doc, err := nearestMap(ctx, a, s, v); err == nil {
		for _, c := range doc.Compositions {
			for _, sc := range c.Scenes {
				for _, w := range sc.Words {
					if w.Key != "" && len(seen[w.Key]) < 6 {
						seen[w.Key] = append(seen[w.Key], c.ID+" · "+sc.Name)
					}
				}
			}
		}
	}
	for _, st := range v.Settings {
		row := RemotionWordRow{Key: st.Key, Code: st.Text, Text: st.Text, File: st.File, Line: st.Line, Name: st.Name, OnScreen: st.OnScreen, Seen: seen[st.Key], Used: []string{}}
		if row.Seen == nil {
			row.Seen = []string{}
		}
		if t, ok := vals[st.Key]; ok && t != st.Text {
			row.Text, row.Changed = t, true
		}
		for i := range v.Compositions {
			if usesKey(&v.Compositions[i], st.Key) {
				row.Used = append(row.Used, v.Compositions[i].ID)
			}
		}
		out.Settings = append(out.Settings, row)
	}
	// A version that makes the words settings, waiting for a person.
	if !out.HasSettings {
		if vs, err := versionsOf(ctx, a, s.ID); err == nil {
			for i := range vs {
				x := &vs[i]
				if x.Number > v.Number && len(x.Settings) > 0 && x.State == models.RemotionWaiting {
					r := versionRow(x)
					out.Waiting = &r
					break
				}
			}
		}
	}
	switch {
	case out.HasSettings:
		n := 0
		for _, r := range out.Settings {
			if r.Changed {
				n++
			}
		}
		out.Message = fmt.Sprintf("The words of “%s” are settings: %s", s.Title, plural(len(out.Settings), "setting", "settings"))
		if n > 0 {
			out.Message += fmt.Sprintf(", %d changed from the code", n)
		}
		out.Message += "."
		out.Next = "Change them here (or in the dashboard): each change is drawn with no network and checked, then the cuts' clips are made again with it."
	case out.Waiting != nil:
		out.Message = fmt.Sprintf("Version %d of “%s” makes its words settings; it waits for a person to approve it.", out.Waiting.Number, s.Title)
		out.Next = "A person approves it under Sources; then its words can be changed as settings."
	case len(out.Copy) > 0:
		var fs []string
		for _, c := range out.Copy {
			fs = append(fs, c.File)
		}
		out.Message = fmt.Sprintf("The words of “%s” are written in its code, in %s that keep them together. MetaRush can make them settings: one change to the code that a person approves, and after that words change without new versions.", s.Title, plural(len(fs), "file", "files"))
		out.Next = "metarush remotion words setup " + s.Name
	default:
		out.Message = fmt.Sprintf("MetaRush found no file that keeps the words of “%s” together; they are written in its scenes.", s.Title)
		out.Next = "Claude can move them into one file of constants first (an ordinary change, checked and approved); then they can become settings."
	}
	return out, nil
}

func wordsSetupOp(ctx context.Context, a *app.App, in *RemotionWordsSetupIn) (*RemotionTaskOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	if len(v.Settings) > 0 {
		return nil, apperr.New(apperr.Conflict, "The words of “%s” are settings already (version %d).", s.Title, v.Number).WithCommand("metarush remotion words " + s.Name)
	}
	if len(v.Copy) == 0 {
		return nil, apperr.New(apperr.Validation, "MetaRush found no file that keeps the words of “%s” together, so it can't make them settings by itself.", s.Title).
			WithHint("Claude can move the words into one file of constants first (an ordinary change, checked and approved).")
	}
	for _, f := range in.Files {
		ok := false
		for _, c := range v.Copy {
			ok = ok || c.File == f
		}
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't one of the files that hold the words of “%s”.", f, s.Title).WithCommand("metarush remotion words " + s.Name)
		}
	}
	extra := map[string]any{}
	if len(in.Files) > 0 {
		extra["files"] = in.Files
	}
	j, err := queueTask(ctx, a, s, v, taskWordsSetup, fmt.Sprintf("making the words of “%s” settings", s.Title), extra, "")
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.words.setup", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "files": in.Files})
	return &RemotionTaskOut{Name: s.Name, JobID: j.ID, State: "queued",
		Message: fmt.Sprintf("Making the change that turns the words of “%s” into settings, as its next version (checked with no network; a person approves it).", s.Title)}, nil
}

func wordsSetOp(ctx context.Context, a *app.App, in *RemotionWordsSetIn) (*RemotionTaskOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	if len(v.Settings) == 0 {
		return nil, apperr.New(apperr.Validation, "The words of “%s” aren't settings yet.", s.Title).
			WithHint("Make them settings first (a one-time change a person approves).").WithCommand("metarush remotion words setup " + s.Name)
	}
	if v.State != models.RemotionApproved {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” isn't approved, so its words can't be changed for cuts yet.", v.Number, s.Title).
			WithHint("A person approves it under Sources first.").WithDetails(map[string]any{"reason": "remotion_not_approved"})
	}
	have := settingByKey(v)
	var set []models.RemotionWordValue
	seen := map[string]bool{}
	for _, w := range in.Set {
		k := strings.TrimSpace(w.Key)
		st, ok := have[k]
		if !ok {
			return nil, apperr.New(apperr.NotFound, "“%s” has no word setting called %s.", s.Title, k).WithCommand("metarush remotion words " + s.Name)
		}
		if seen[k] {
			return nil, apperr.New(apperr.InvalidInput, "%s is changed twice.", k)
		}
		seen[k] = true
		t := strings.TrimSpace(w.Text)
		if len([]rune(t)) > maxWordLength {
			return nil, apperr.New(apperr.InvalidInput, "The new words for %s are too long (more than %d characters).", k, maxWordLength)
		}
		if t == "" {
			t = st.Text // empty: back to the code's words
		}
		set = append(set, models.RemotionWordValue{Key: k, Text: t})
	}
	sort.Slice(set, func(i, j int) bool { return set[i].Key < set[j].Key })
	b, _ := json.Marshal(set)
	what := fmt.Sprintf("checking new words for “%s”", s.Title)
	if !in.CheckOnly {
		what = fmt.Sprintf("changing words of “%s”", s.Title)
	}
	j, err := queueTask(ctx, a, s, v, taskWords, what, map[string]any{"set": string(b), "note": strings.TrimSpace(in.Note), "checkOnly": in.CheckOnly}, "")
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.words.set", "remotion", s.ID, s.ProjectID, map[string]any{"keys": len(set), "checkOnly": in.CheckOnly})
	msg := fmt.Sprintf("Drawing “%s” with the new words (no network) to check them", s.Title)
	if !in.CheckOnly {
		msg += "; then the cuts' clips are made again with them"
	}
	return &RemotionTaskOut{Name: s.Name, JobID: j.ID, State: "queued", Message: msg + "."}, nil
}

// ---- the one-time change (D168) ----

func runWordsSetup(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	var files []string
	if xs, ok := j.Payload["files"].(bson.A); ok {
		for _, x := range xs {
			if f, ok := x.(string); ok {
				files = append(files, f)
			}
		}
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	work, done, err := workTree(ctx, a, v, m, "")
	if err != nil {
		return nil, err
	}
	report(0.05, "writing the change")
	args := []string{"render/source/analyze.mjs", "words-setup", "--work", work}
	for _, f := range files {
		args = append(args, "--file", f)
	}
	var res struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Files   []struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"files"`
		Helper string `json:"helper"`
		Consts []struct {
			File  string `json:"file"`
			Name  string `json:"name"`
			Words int    `json:"words"`
		} `json:"consts"`
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, rerr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: args, MaxLine: 64 << 20, OnLine: func(l string) {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && (ev.Type == "words-setup" || ev.Type == "error") {
			_ = json.Unmarshal([]byte(l), &res)
		}
	}})
	done()
	if res.Type != "words-setup" {
		msg := res.Message
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		return nil, finalf("Writing the change didn't work: %s", firstLine(msg))
	}
	if len(res.Files) == 0 {
		return nil, finalf("MetaRush found no words to make settings in “%s”.", s.Title)
	}
	// The next version: the base's files with the changed ones in their place.
	dir, err := tempDir(a, "remotion-words-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	byPath := map[string]ManifestFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	var send []snapFile
	var changed []string
	for i, f := range res.Files {
		if !safeRel(f.Path) || isPublic(f.Path) {
			return nil, finalf("the change names an unusual file (%s)", f.Path)
		}
		p := filepath.Join(dir, fmt.Sprintf("%03d", i))
		if err := os.WriteFile(p, []byte(f.Text), 0o644); err != nil {
			return nil, err
		}
		mf := ManifestFile{Path: f.Path, Size: int64(len(f.Text)), SHA256: sha256Hex([]byte(f.Text))}
		byPath[f.Path] = mf
		send = append(send, snapFile{ManifestFile: mf, local: p})
		changed = append(changed, f.Path)
	}
	if err := uploadFiles(ctx, a, send, nil); err != nil {
		return nil, err
	}
	var list []ManifestFile
	for _, f := range byPath {
		list = append(list, f)
	}
	nm := NewManifest(list)
	mobj, err := putBytes(ctx, a, nm.Bytes(), "application/json")
	if err != nil {
		return nil, err
	}
	nf, nb, pf, pb := totals(nm)
	var names []string
	for _, c := range res.Consts {
		names = append(names, c.Name)
	}
	msg := fmt.Sprintf("The words on screen become settings (%s)", joinAnd(names))
	nv := &models.RemotionVersion{Hash: nm.Hash(), Parent: v.Hash, Kind: models.RemotionKindEdit, Message: clipText(msg, 2000), By: a.Actor, At: time.Now().UTC(),
		Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: nf, Bytes: nb, PublicFiles: pf, PublicBytes: pb}
	nv, _, err = insertVersion(ctx, a, s, nv)
	if err != nil {
		return nil, err
	}
	report(0.15, "checking the new version")
	rep, err := checkVersion(ctx, a, s, nv, checkOptions{Message: msg, Say: func(m string) { report(0.5, m) }})
	if err != nil {
		return nil, err
	}
	fresh, _ := getVersion(ctx, a, nv.Hash)
	if fresh != nil {
		nv = fresh
	}
	out := fmt.Sprintf("Version %d of “%s” makes its words settings (%s changed: %s). ", nv.Number, s.Title, plural(len(changed), "file", "files"), joinAnd(changed))
	switch {
	case rep.Check != nil && !rep.Check.OK:
		out += "It didn't pass its check: " + rep.Check.Summary
	case nv.State == models.RemotionApproved:
		out += "It is approved already."
	default:
		out += "It waits for a person to approve it; the pictures should look the same as before."
	}
	report(1, out)
	return map[string]any{"message": out, "version": nv.Hash, "versionNumber": nv.Number, "projectId": s.ProjectID, "sourceId": s.ID, "ok": rep.Check == nil || rep.Check.OK}, nil
}

// ---- changing words (D169) ----

func runWordsTask(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	var set []models.RemotionWordValue
	if raw, _ := j.Payload["set"].(string); raw != "" {
		_ = json.Unmarshal([]byte(raw), &set)
	}
	note, _ := j.Payload["note"].(string)
	checkOnly, _ := j.Payload["checkOnly"].(bool)
	if len(set) == 0 {
		return nil, finalf("No words to change.")
	}
	fresh, err := getSource(ctx, a, s.ID)
	if err != nil {
		return nil, &Final{Err: err}
	}
	s = fresh
	have := settingByKey(v)
	// The settings before and after.
	before := map[string]string{}
	if s.Words != nil {
		for _, w := range s.Words.Values {
			before[w.Key] = w.Text
		}
	}
	after := map[string]string{}
	for k, t := range before {
		after[k] = t
	}
	var changes []string
	for _, w := range set {
		st := have[w.Key]
		old := st.Text
		if t, ok := before[w.Key]; ok {
			old = t
		}
		if w.Text == st.Text {
			delete(after, w.Key)
		} else {
			after[w.Key] = w.Text
		}
		if old != w.Text {
			changes = append(changes, fmt.Sprintf("“%s” became “%s”", clip(old, 80), clip(w.Text, 80)))
		}
	}
	nextNumber := 1
	if s.Words != nil {
		nextNumber = s.Words.Number + 1
	}
	// Check: draw the compositions that read them, before and after.
	report(0.05, "drawing the new words with no network")
	chk, err := checkWords(ctx, a, s, v, set, before, after, report)
	if err != nil {
		var er *errRun
		if asErrRun(err, &er) {
			chk = &models.RemotionWordsCheck{OK: false, Version: v.Hash, Problems: []models.RemotionProblem{{Severity: "error", Code: "render", Message: "MetaRush couldn't draw it with the new words: " + er.msg + "."}}}
		} else {
			return nil, err
		}
	}
	summary := wordsSummary(changes, chk)
	change := models.RemotionWordsChange{Number: nextNumber, At: time.Now().UTC(), By: a.Actor, Set: set, Note: note, Summary: summary, Check: chk}
	if checkOnly || !chk.OK {
		change.Number = 0
		_, _ = a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"wordsPending": change, "updatedAt": time.Now().UTC()}})
		msg := summary
		if !chk.OK {
			msg += " Nothing was changed."
		}
		report(1, msg)
		return map[string]any{"message": msg, "ok": chk.OK, "checkOnly": checkOnly, "projectId": s.ProjectID, "sourceId": s.ID}, nil
	}
	// Record it (compare-and-swap on the number of changes).
	var values []models.RemotionWordValue
	for k, t := range after {
		values = append(values, models.RemotionWordValue{Key: k, Text: t})
	}
	sort.Slice(values, func(i, k int) bool { return values[i].Key < values[k].Key })
	if values == nil {
		values = []models.RemotionWordValue{}
	}
	hist := []models.RemotionWordsChange{change}
	if s.Words != nil {
		hist = append(hist, s.Words.Changes...)
	}
	if len(hist) > 50 {
		hist = hist[:50]
	}
	w := models.RemotionWords{Number: nextNumber, Values: values, At: change.At, By: a.Actor, Changes: hist}
	filter := bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}
	if s.Words == nil {
		filter["words"] = bson.M{"$exists": false}
	} else {
		filter["words.number"] = s.Words.Number
	}
	res, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"words": w, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"wordsPending": ""}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, finalf("Someone else changed the words of “%s” at the same moment; look at them again and change them once more.", s.Title)
	}
	s.Words = &w
	a.Audit(ctx, "remotion.words.change", "remotion", s.ID, s.ProjectID, map[string]any{"number": w.Number, "keys": len(set)})
	// The latest cuts' clips move to renders with the new words.
	report(0.9, "making the cuts' clips again with the new words")
	var moved []string
	for _, pid := range projectsUsing(ctx, a, s) {
		r, err := updateProject(ctx, a, s, pid, "main")
		if err != nil {
			moved = append(moved, "moving a cut's clips didn't work: "+apperrMessage(err))
			continue
		}
		if r.Moved > 0 || r.Waiting > 0 {
			moved = append(moved, r.Message)
		}
	}
	msg := summary
	if len(moved) > 0 {
		msg += " " + strings.Join(moved, " ")
	}
	report(1, msg)
	return map[string]any{"message": msg, "ok": true, "number": w.Number, "projectId": s.ProjectID, "sourceId": s.ID}, nil
}

// wordsSummary says what a change of words does, in plain words.
func wordsSummary(changes []string, chk *models.RemotionWordsCheck) string {
	var b strings.Builder
	if len(changes) == 0 {
		b.WriteString("No words change.")
	} else {
		b.WriteString(upperFirst(joinAnd(changes)) + ".")
	}
	for _, l := range chk.Lengths {
		if math.Abs(l.After-l.Before) > 0.001 {
			fmt.Fprintf(&b, " %s is now %s long (it was %s).", l.Composition, clock(l.After), clock(l.Before))
		}
	}
	errs, warns := 0, 0
	for _, p := range chk.Problems {
		if p.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	for _, p := range chk.Problems {
		b.WriteString(" " + strings.TrimSpace(p.Message))
	}
	if errs == 0 && warns == 0 && len(chk.Stills) > 0 {
		b.WriteString(" The new words fit where they appear.")
	}
	return b.String()
}

// checkWords draws the compositions that read the changed words, before and
// after (the probe reads the words on screen and whether they fit), and
// compares their lengths.
func checkWords(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, set []models.RemotionWordValue, before, after map[string]string, report func(float64, string)) (*models.RemotionWordsCheck, error) {
	chk := &models.RemotionWordsCheck{OK: true, Version: v.Hash}
	have := settingByKey(v)
	keys := map[string]bool{}
	for _, w := range set {
		keys[w.Key] = true
	}
	// The compositions that read them.
	var comps []models.RemotionComposition
	for _, c := range v.Compositions {
		if c.Still {
			continue
		}
		for k := range keys {
			if usesKey(&c, k) {
				comps = append(comps, c)
				break
			}
		}
	}
	if len(comps) == 0 {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "words_unused", Message: "No composition's code reads these words, so nothing on screen changes."})
		return chk, nil
	}
	sort.SliceStable(comps, func(i, j int) bool { return comps[i].Duration < comps[j].Duration })
	if len(comps) > maxCheckCompositions {
		comps = comps[:maxCheckCompositions]
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	pi, err := versionPackages(ctx, a, m)
	if err != nil {
		return nil, err
	}
	env, err := ensureEnv(ctx, a, pi, videoDir, nil)
	if err != nil {
		return nil, err
	}
	work, done, err := workTree(ctx, a, v, m, env)
	if err != nil {
		return nil, err
	}
	defer done()
	ff, err := fontFiles(ctx, a, v.Fonts)
	if err != nil {
		return nil, err
	}
	// The pictures each composition draws now (D209), so the check draws what renders will.
	pics := map[string][]models.RemotionPictureValue{}
	var serve []models.RemotionPictureValue
	for _, c := range comps {
		_, vals, _ := picturesFor(s, v, c.ID, nil)
		pics[c.ID] = vals
		serve = append(serve, vals...)
	}
	picFiles, err := pictureFiles(ctx, a, serve)
	if err != nil {
		return nil, err
	}
	propsOf := func(vals map[string]string, c models.RemotionComposition) map[string]any {
		use := map[string]string{}
		for k, t := range vals {
			if st, ok := have[k]; ok && st.Text != t && usesKey(&c, k) {
				use[k] = t
			}
		}
		words := ""
		if len(use) > 0 {
			b, _ := json.Marshal(use)
			words = string(b)
		}
		return renderProps("", words, pics[c.ID])
	}
	// Where the words are on screen: the map's scenes that show them, else the middle.
	doc, _ := loadMap(ctx, a, v)
	frames := map[string][]int{}
	for _, c := range comps {
		var fs []int
		if doc != nil {
			for _, mc := range doc.Compositions {
				if mc.ID != c.ID {
					continue
				}
				for _, sc := range mc.Scenes {
					for _, w := range sc.Words {
						if (w.Setting && keys[w.Key]) || (!w.Setting && wordMatches(w.Text, set, have)) {
							fs = append(fs, int(math.Round(sc.StillAt*c.FPS)))
							break
						}
					}
				}
			}
		}
		if len(fs) == 0 {
			fs = []int{c.DurationInFrames / 2, c.DurationInFrames * 2 / 3}
		}
		fs = uniqueInts(fs)
		if len(fs) > stillsPerComposition {
			fs = fs[:stillsPerComposition]
		}
		frames[c.ID] = fs
	}
	out, err := tempDir(a, "remotion-words-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	bundle := bundleDir(a, v) + "-map"
	var tasks []runTask
	for _, c := range comps {
		var bi, ai []stillItem
		for _, f := range frames[c.ID] {
			name := fmt.Sprintf("%s-%05d", sanitizeName(c.ID), f)
			scale := fitScale(c.Width, 960)
			bi = append(bi, stillItem{Composition: c.ID, Frame: f, Out: filepath.Join(out, name+"-before.jpg"), Scale: scale})
			ai = append(ai, stillItem{Composition: c.ID, Frame: f, Out: filepath.Join(out, name+"-after.jpg"), Scale: scale})
		}
		tasks = append(tasks,
			runTask{Type: "compositions", Bundle: "before", Props: propsOf(before, c)},
			runTask{Type: "compositions", Bundle: "after", Props: propsOf(after, c)},
			runTask{Type: "stills", Bundle: "before", Props: propsOf(before, c), Items: bi, Probe: "text"},
			runTask{Type: "stills", Bundle: "after", Props: propsOf(after, c), Items: ai, Probe: "text"})
	}
	b := func(name string) runBundle {
		return runBundle{Name: name, Work: work, Entry: v.Entry, Bundle: bundle, Fonts: ff, Probe: true, Pictures: picFiles}
	}
	res, err := jailRunEach(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: checkSeconds, FrameTimeoutMs: frameTimeout(v), Bundles: []runBundle{b("before"), b("after")}, Tasks: tasks})
	if err != nil {
		return nil, err
	}
	if len(res.blocked) > 0 {
		chk.OK = false
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "network", Message: "Its code tried to reach " + joinAnd(dedupe(res.blocked)) + " while it was drawn; renders have no network, so MetaRush blocked it."})
	}
	// Lengths.
	for _, c := range comps {
		composition := c.ID // the project's own name for it
		bl, al := res.lengths["before"][composition], res.lengths["after"][composition]
		if bl > 0 && al > 0 {
			chk.Lengths = append(chk.Lengths, models.RemotionLengthChange{Composition: composition, Before: round3(bl), After: round3(al)})
			if al > MaxSeconds {
				chk.OK = false
				chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "too_long", Message: fmt.Sprintf("With the new words “%s” is %s long; clips are at most 15 minutes.", composition, clock(al))})
			}
		}
	}
	// Words that don't fit, and the stills.
	newTexts := map[string]bool{}
	for _, w := range set {
		newTexts[normWords(w.Text)] = true
	}
	for _, c := range comps {
		for _, f := range frames[c.ID] {
			name := fmt.Sprintf("%s-%05d", sanitizeName(c.ID), f)
			bp, ap := filepath.Join(out, name+"-before.jpg"), filepath.Join(out, name+"-after.jpg")
			ae, ok := res.stills[ap]
			if !ok {
				continue
			}
			st := models.RemotionStill{Composition: c.ID, Frame: f, At: round3(float64(f) / math.Max(1, c.FPS))}
			if obj, err := putLocal(ctx, a, ap, "image/jpeg"); err == nil {
				obj.Width, obj.Height = ae.Width, ae.Height
				st.After = obj
			}
			if _, ok := res.stills[bp]; ok {
				if obj, err := putLocal(ctx, a, bp, "image/jpeg"); err == nil {
					obj.Width, obj.Height = ae.Width, ae.Height
					st.Before = &obj
				}
				if d, err := frameDiff(bp, ap); err == nil {
					st.Difference = math.Round(d*1000) / 1000
				}
			}
			chk.Stills = append(chk.Stills, st)
			if ae.Text == nil {
				continue
			}
			for _, p := range phrases(ae.Text.Items) {
				if len(p.Flags) == 0 || !phraseIsNew(p.Text, newTexts) {
					continue
				}
				chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "words_overflow",
					Message: fmt.Sprintf("In “%s” at %s, “%s” doesn't fit: %s.", st.Composition, clock(st.At), clip(p.Text, 80), overflowWords(p.Flags)),
					Hint:    "Shorter words fit; or change the box in the code (an ordinary change a person approves)."})
			}
		}
	}
	return chk, nil
}

func overflowWords(flags []string) string {
	var out []string
	for _, f := range flags {
		switch f {
		case "outside":
			out = append(out, "it runs past the edge of the picture")
		case "clipped":
			out = append(out, "its box cuts it off")
		case "cut short":
			out = append(out, "it is cut short with “…”")
		}
	}
	if len(out) == 0 {
		return "it spills out of its box"
	}
	return joinAnd(out)
}

// phraseIsNew reports words on screen that are (part of) a new setting.
func phraseIsNew(text string, newTexts map[string]bool) bool {
	n := normWords(text)
	for t := range newTexts {
		if t != "" && (strings.Contains(n, t) || strings.Contains(t, n)) {
			return true
		}
	}
	return false
}

// wordMatches reports words on screen that are one of the changed settings' code words.
func wordMatches(text string, set []models.RemotionWordValue, have map[string]models.RemotionWordSetting) bool {
	n := normWords(text)
	for _, w := range set {
		if c := normWords(have[w.Key].Text); c != "" && (c == n || strings.Contains(c, n) || strings.Contains(n, c)) {
			return true
		}
	}
	return false
}

func uniqueInts(xs []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, x := range xs {
		if x >= 0 && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Ints(out)
	return out
}

// eachResult is a run with several bundles: lengths and stills per bundle.
type eachResult struct {
	lengths map[string]map[string]float64 // bundle → composition → seconds
	stills  map[string]runEvent           // out path → still
	blocked []string
}

// jailRunEach runs a task file and sorts what it reported by bundle.
func jailRunEach(ctx context.Context, a *app.App, videoDir string, spec runSpec) (*eachResult, error) {
	out := &eachResult{lengths: map[string]map[string]float64{}, stills: map[string]runEvent{}}
	res, err := jailRun(ctx, a, videoDir, spec, func(ev runEvent) {
		if ev.Type == "compositions" {
			name := firstNonEmpty(ev.Bundle, "main")
			if out.lengths[name] == nil {
				out.lengths[name] = map[string]float64{}
			}
			for _, c := range ev.Compositions {
				if c.FPS > 0 {
					out.lengths[name][c.ID] = float64(c.DurationInFrames) / c.FPS
				}
			}
		}
	})
	if res != nil {
		out.blocked = res.Blocked
		for _, ev := range res.Stills {
			out.stills[ev.Out] = ev
		}
	}
	return out, err
}

var _ = links.Remotion
