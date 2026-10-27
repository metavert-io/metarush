package recipes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/drive"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Drafts (D206, D212): recipe.update builds a draft from a version — the newest
// draft unless a version is named — with a merge patch, a new title, hints and
// rules to add. A dry run says what the draft would change in plain words and
// saves nothing: `recipe update --dry-run`, and the card Claude's change
// becomes in the panel (D211).

// draftOf works out the document recipe.update makes and the version it
// starts from (nothing is saved).
func draftOf(ctx context.Context, a *app.App, in *RecipeUpdateIn) (*models.Recipe, *models.RecipeVersion, *models.RecipeDoc, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, nil, nil, err
	}
	base, err := loadVersion(ctx, a, r, in.Version)
	if err != nil {
		return nil, nil, nil, err
	}
	if in.Version == "" && r.Draft != "" {
		if d, err := loadVersion(ctx, a, r, "draft"); err == nil {
			base = d // build on the newest draft
		}
	}
	doc := Clone(&base.Doc)
	if p := bytes.TrimSpace(in.Patch); len(p) > 0 && string(p) != "null" {
		merged, err := mergePatch(mustJSON(doc), p)
		if err != nil {
			return nil, nil, nil, err
		}
		var next models.RecipeDoc
		dec := json.NewDecoder(bytes.NewReader(merged))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&next); err != nil {
			return nil, nil, nil, apperr.Wrap(apperr.InvalidInput, err, "The change doesn't fit a recipe (a field is unknown or has the wrong kind of value).").WithDetails(map[string]any{"problem": err.Error()})
		}
		doc = &next
	}
	if t := strings.TrimSpace(in.Title); t != "" {
		doc.Title = shorten(t, 120)
	}
	for _, h := range in.AddHints {
		if h = strings.TrimSpace(h); h != "" && !containsString(doc.Hints, h) {
			doc.Hints = append(doc.Hints, shorten(h, 300))
		}
	}
	for _, t := range in.AddRules {
		if t = strings.TrimSpace(t); t != "" {
			doc.Rules = append(doc.Rules, models.RecipeRule{Text: shorten(t, 300), Measure: ops.MeasureFor(t)})
		}
	}
	if err := setDriveFolders(doc, in.FromDrive, drive.DriveType); err != nil {
		return nil, nil, nil, err
	}
	if err := setDriveFolders(doc, in.FromDropbox, drive.DropboxType); err != nil {
		return nil, nil, nil, err
	}
	doc.Format = models.RecipeFormat
	doc.Makes = MakesWords(doc)
	return r, base, doc, nil
}

// draftPreview says what a draft would change from the version it starts
// from; nothing is saved.
func draftPreview(ctx context.Context, a *app.App, r *models.Recipe, base *models.RecipeVersion, doc *models.RecipeDoc, chk *models.RecipeCheck) *RecipeUpdateOut {
	out := &RecipeUpdateOut{Version: versionRow(base, nil), Check: chk, Recipe: r.Name, Title: r.Title, DryRun: true}
	if sameDoc(doc, &base.Doc) {
		out.Same = true
		out.Message = fmt.Sprintf("Nothing would change: that is version %d of “%s”.", base.Number, r.Title)
		return out
	}
	out.Changes, out.RemovedRules = Differences(ctx, a, &base.Doc, doc), removedRules(&base.Doc, doc)
	n := countChanges(out.Changes)
	switch {
	case !chk.OK:
		out.Message = fmt.Sprintf("A draft from version %d of “%s” would change %s; its check finds %s to fix. Nothing is saved yet.", base.Number, r.Title, plural(n, "thing"), plural(len(chk.Problems), "problem"))
	default:
		out.Message = fmt.Sprintf("A draft from version %d of “%s” would change %s. Nothing is saved yet.", base.Number, r.Title, plural(n, "thing"))
		out.Next = "Save it as a draft; a person makes it current."
	}
	return out
}

// InputsFromFlags reads what a run gives its inputs, written name=value
// (`--input headline="Read free"`; a name given again adds to its clips or
// pictures, and commas separate several): the CLI's flags and the commands
// Claude asks for in the panel (D211) read them the same way.
func InputsFromFlags(raw []string) ([]RecipeInputValue, error) {
	var out []RecipeInputValue
	at := map[string]int{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("give each input as name=value, like headline=\"Read free\"")
		}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, RecipeInputValue{Name: k})
			i = len(out) - 1
		}
		out[i].Text = v
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out[i].Items = append(out[i].Items, item)
			}
		}
	}
	return out, nil
}
