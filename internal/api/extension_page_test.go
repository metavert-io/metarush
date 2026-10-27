package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

func pagePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 200; x++ {
			img.Set(x, y, color.RGBA{0xe2, 0x55, byte(y), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// "Send this page" (D105): a browser paired since pages has the pages scope;
// the page becomes a website snapshot in the workspace library and the
// answer is words only. A browser paired before scopes can still send video,
// and is told to pair again for pages.
func TestExtensionSendsPage(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	code := pairCode(t, d)
	in, _ := json.Marshal(ExtPairIn{Code: code, Name: "Chrome", Version: "0.2.0"})
	_, out, _ := d.ext("POST", "/ext/pair", extOrigin, "", in, nil)
	var p ExtPairOut
	_ = json.Unmarshal(out.Data, &p)

	page, _ := json.Marshal(ExtPageIn{URL: "https://example.com/pricing#plans", Title: "Widget pricing", ThemeColor: "#e2552d", Text: "Pricing\nFree for one person.", Screenshot: pagePNG(t)})
	st, out, _ := d.ext("POST", "/ext/pages", extOrigin, p.Token, page, nil)
	var po ExtPageOut
	_ = json.Unmarshal(out.Data, &po)
	if st != http.StatusOK || po.Status != "added" || !strings.Contains(po.Message, "Widget pricing") || strings.Contains(string(out.Data), "res_") {
		t.Fatalf("send page: %d %s", st, out.Data)
	}
	var r models.Resource
	if err := d.a.DB.C(store.CResources).FindOne(context.Background(), bson.M{"workspaceId": d.a.WorkspaceID(), "kind": models.ResourceWebsite}).Decode(&r); err != nil {
		t.Fatal(err)
	}
	v := r.Latest()
	if r.Scope != models.ScopeWorkspace || r.CreatedBy.Via != models.ViaExtension || v.Site.URL != "https://example.com/pricing" || !v.Site.SignedIn || v.Text == "" {
		t.Fatalf("the snapshot: %+v %+v", r, v.Site)
	}

	// Refusals: a screenshot that isn't a PNG, a page on this computer.
	bad, _ := json.Marshal(ExtPageIn{URL: "https://example.com/", Screenshot: "data:image/png;base64,bm90IGEgcG5n"})
	if st, _, _ := d.ext("POST", "/ext/pages", extOrigin, p.Token, bad, nil); st != http.StatusBadRequest {
		t.Fatalf("not a picture: %d", st)
	}
	local, _ := json.Marshal(ExtPageIn{URL: "http://127.0.0.1:4845/", Screenshot: pagePNG(t)})
	if st, _, _ := d.ext("POST", "/ext/pages", extOrigin, p.Token, local, nil); st != http.StatusBadRequest {
		t.Fatalf("a page on this computer: %d", st)
	}

	// A browser paired before pages existed (no scopes recorded): video only.
	state := filepath.Join(d.a.Cfg.StateDir(), "browser-extension.json")
	b, _ := os.ReadFile(state)
	var sf map[string]any
	_ = json.Unmarshal(b, &sf)
	for _, br := range sf["browsers"].([]any) {
		delete(br.(map[string]any), "scopes")
	}
	b, _ = json.Marshal(sf)
	_ = os.WriteFile(state, b, 0o600)
	st, out, _ = d.ext("POST", "/ext/pages", extOrigin, p.Token, page, nil)
	if st != http.StatusForbidden || out.Error.Code != "E_CONSENT_REQUIRED" {
		t.Fatalf("an older pairing can't send pages: %d %+v", st, out.Error)
	}
	if st, _, _ := d.ext("POST", "/ext/hello", extOrigin, p.Token, []byte(`{}`), nil); st != http.StatusOK {
		t.Fatalf("an older pairing still works for video: %d", st)
	}
}
