package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// Recipe files from the dashboard (D219): the file comes to the browser as a
// download, and goes back up as the body of an import — signed in, the CSRF
// header on the import, the server choosing every path; recipe.export and
// recipe.import themselves stay off /api/ops (LocalOnly).
func TestRecipeFilesThroughTheDashboard(t *testing.T) {
	d, projectID, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	ctx := context.Background()
	saved, err := ops.CallTyped[recipes.RecipeSaveOut](ctx, d.a, "recipe.save", recipes.RecipeSaveIn{From: projectID}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	name := saved.Recipe.Name
	get := func() *http.Response {
		res, err := d.client.Get(d.base + "/api/recipes/export?recipe=" + name)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := get(); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", res.StatusCode)
	}
	d.signIn()
	res := get()
	file, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !strings.Contains(res.Header.Get("Content-Disposition"), name+".recipe.zip") {
		t.Fatalf("download: %d %v", res.StatusCode, res.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil || len(zr.File) < 2 || zr.File[0].Name != "recipe.json" {
		t.Fatalf("the file: %v", err)
	}
	upload := func(query string, csrf bool) (int, envelope) {
		req, _ := http.NewRequest("POST", d.base+"/api/recipes/import"+query, bytes.NewReader(file))
		if csrf {
			req.Header.Set(clientHeader, "dashboard")
		}
		req.Header.Set("Content-Type", "application/zip")
		res, err := d.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var env envelope
		_ = json.NewDecoder(res.Body).Decode(&env)
		return res.StatusCode, env
	}
	if code, env := upload("?dryRun=1", false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	code, env := upload("?dryRun=1&fileName="+name+".recipe.zip", true)
	var out recipes.RecipeImportOut
	_ = json.Unmarshal(env.Data, &out)
	if code != 200 || !out.Same || out.Name != name {
		t.Fatalf("its own file comes back as the version it is: %d %+v %+v", code, env.Error, out)
	}
	for _, op := range []string{"recipe.export", "recipe.import"} {
		if code, _ := d.do("POST", "/api/ops/"+op, map[string]any{"recipe": name, "file": "/etc/passwd"}, true); code == 200 {
			t.Fatalf("%s over /api/ops", op)
		}
	}
}

// The Recipes feature's file endpoints refuse plainly what they can't take:
// no recipe or an unknown one, no file, a file larger than the dashboard
// takes, or one that isn't a recipe file.
func TestRecipeFileEndpointsRefusePlainly(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	d.signIn()
	refused := func(what string, code int, b []byte, want int, words string) {
		t.Helper()
		env := envOf(t, b)
		if code != want || env.OK || !strings.Contains(env.Error.Message, words) {
			t.Fatalf("%s: %d %s", what, code, b)
		}
	}
	code, _, b := d.send("GET", "/api/recipes/export", nil, nil, false)
	refused("no recipe", code, b, http.StatusBadRequest, "Say which recipe")
	code, _, b = d.send("GET", "/api/recipes/export?recipe=no-such-recipe&maxMb=5", nil, nil, false)
	if env := envOf(t, b); code != http.StatusNotFound || env.Error.Code != apperr.NotFound {
		t.Fatalf("an unknown recipe: %d %s", code, b)
	}
	code, _, b = d.send("POST", "/api/recipes/import", nil, map[string]string{"Content-Type": "application/zip"}, true)
	refused("no file", code, b, http.StatusBadRequest, "Choose a recipe file")
	old := maxRecipeUpload
	maxRecipeUpload = 8
	t.Cleanup(func() { maxRecipeUpload = old })
	code, _, b = d.send("POST", "/api/recipes/import", []byte("0123456789"), map[string]string{"Content-Type": "application/zip"}, true)
	maxRecipeUpload = old
	refused("too large", code, b, http.StatusBadRequest, "larger than the dashboard takes")
	code, _, b = d.send("POST", "/api/recipes/import?dryRun=1", []byte("PK\x03\x04 not a zip"), map[string]string{"Content-Type": "application/zip"}, true)
	if env := envOf(t, b); code < 400 || code >= 500 || env.OK {
		t.Fatalf("a malformed recipe file: %d %s", code, b)
	}
}
