package resources

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func screenPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, c)
			if y < 30 {
				img.Set(x, y, color.RGBA{0x1b, 0x1b, 0x1f, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPageHelpers(t *testing.T) {
	if canonicalPage("https://Example.com/#top") != "https://example.com" || canonicalPage("https://example.com/pricing#plans") != "https://example.com/pricing" {
		t.Fatal("canonical pages")
	}
	for raw, want := range map[string]string{"https://www.example.com/": "example-com", "https://example.com/docs/getting-started.html": "example-com-docs-getting-started", "http://127.0.0.1:5126/": "r-127-0-0-1"} {
		u, _ := url.Parse(raw)
		if got := pageSlug(u); got != want {
			t.Errorf("%s: %s, want %s", raw, got, want)
		}
	}
	if normColor("#e2552d") != "#E2552D" || normColor("#fa0") != "#FFAA00" || normColor("red") != "" {
		t.Fatal("colours")
	}
	for _, h := range []string{"localhost", "nas", "my-mac.local", "127.0.0.1", "10.1.2.3", "192.168.1.5", "169.254.1.1", "::1", "fd00::1", "100.64.0.1",
		"::", "0.1.2.3", "198.18.0.1", "224.0.0.1", "255.255.255.255", "fec0::1", "ff02::1", "fe80::1",
		"::ffff:7f00:1", "[::ffff:127.0.0.1]", "::7f00:1", "::127.0.0.1", "64:ff9b::a00:1", "2002:c0a8:101::1", "::ffff:1:2:3:4:5:6"} {
		if !LocalAddress(h) {
			t.Errorf("%s should be local", h)
		}
	}
	for _, h := range []string{"example.com", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "64:ff9b::5db8:d822", "2002:5db8:d822::1", "8.8.8.8"} {
		if LocalAddress(h) {
			t.Errorf("%s should not be local", h)
		}
	}
	good := screenPNG(t, color.RGBA{0xe2, 0x55, 0x2d, 255})
	if b, err := DecodeDataURL("data:image/png;base64,"+base64.StdEncoding.EncodeToString(good), 1<<20); err != nil || !bytes.Equal(b, good) {
		t.Fatalf("a PNG data address: %v", err)
	}
	for _, bad := range []string{"data:image/jpeg;base64,AAAA", "data:image/png;base64,!!!", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a png"))} {
		if _, err := DecodeDataURL(bad, 1<<20); err == nil {
			t.Errorf("%.40s should be refused", bad)
		}
	}
	if _, err := DecodeDataURL("data:image/png;base64,"+base64.StdEncoding.EncodeToString(good), 100); err == nil {
		t.Fatal("too large")
	}
}

// What the browser extension sends becomes a dated website snapshot in the
// workspace library; the same page again is a new version, the same picture
// again is nothing new.
func TestPageCapture(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	c := PageCapture{URL: "https://example.com/pricing", Title: "Widget pricing", Description: "Free for one person.", ThemeColor: "#e2552d", Lang: "en",
		Text: "Pricing\n\nFree for one person.", PNG: screenPNG(t, color.RGBA{0xe2, 0x55, 0x2d, 255})}
	r, status, err := AddPageCapture(ctx, a, c)
	if err != nil {
		t.Fatal(err)
	}
	v := r.Latest()
	if status != StatusAdded || r.Kind != models.ResourceWebsite || r.Scope != models.ScopeWorkspace || r.Name != "example-com-pricing" || r.Title != "Widget pricing" {
		t.Fatalf("added: %s %+v", status, r)
	}
	if v.Site == nil || v.Site.Via != "extension" || !v.Site.SignedIn || v.Site.URL != "https://example.com/pricing" || v.Site.ThemeColors[0] != "#E2552D" {
		t.Fatalf("site: %+v", v.Site)
	}
	if len(v.Pages) != 1 || v.Pages[0].Label != models.ShotDesktop || v.Text != "Pricing\n\nFree for one person." || v.Source.Kind != models.FromURL || v.Source.Site != "example.com" {
		t.Fatalf("version: pages %+v text %q source %+v", v.Pages, v.Text, v.Source)
	}
	if !strings.HasPrefix(v.Source.Provenance(), "example.com/pricing, ") {
		t.Fatalf("provenance %q", v.Source.Provenance())
	}
	hasWebsiteTag := false
	for _, tag := range r.AutoTags {
		hasWebsiteTag = hasWebsiteTag || tag.Tag == "website"
	}
	if !hasWebsiteTag {
		t.Fatalf("tags: %+v", r.AutoTags)
	}
	if _, st, err := AddPageCapture(ctx, a, c); err != nil || st != StatusSame {
		t.Fatalf("the same picture again: %s %v", st, err)
	}
	c.PNG = screenPNG(t, color.RGBA{0x2d, 0x6a, 0xe2, 255})
	r2, st, err := AddPageCapture(ctx, a, c)
	if err != nil || st != StatusVersion || r2.ID != r.ID || len(r2.Versions) != 2 {
		t.Fatalf("a newer snapshot: %s %+v %v", st, r2, err)
	}
}
