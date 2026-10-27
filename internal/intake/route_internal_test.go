package intake

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
)

// Every pasted address is routed where it belongs, and the person reads what
// MetaRush will do with it (or why it can't) in plain words.
func TestRouteLinkSaysWhereEachLinkGoes(t *testing.T) {
	t.Setenv("METARUSH_PAGES_ALLOW_LOCAL", "")
	cases := []struct {
		in, route, platform, message string
		ready                        bool
	}{
		{"hello world", "invalid", "", "That isn't a web address MetaRush can use.", false},
		{"ftp://example.com/file.mov", "invalid", "", "That isn't a web address MetaRush can use.", false},
		{"https://youtu.be/", "invalid", "", "There's no video at that address.", false},
		{"http://localhost:8080/page", "invalid", "", "That link points at this computer or a local network, so MetaRush won't download from it.", false},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", resources.LinkVideo, models.PlatformYouTube, "A video on YouTube: MetaRush downloads it and adds it as footage.", true},
		{"https://vimeo.com/76979871", resources.LinkVideo, models.PlatformWeb, "A video on the web: MetaRush downloads it and adds it as footage.", true},
		{"https://example.com/talks/keynote.mp4", resources.LinkVideo, models.PlatformWeb, "A video on the web: MetaRush downloads it and adds it as footage.", true},
		{"https://github.com/acme/widget", resources.LinkRepo, models.PlatformWeb, "A GitHub repository: MetaRush keeps a read-only copy of its files as they are now, for code slides, diagrams and a stats card.", true},
		{"https://example.com/brand/logo.SVG", resources.LinkPage, models.PlatformWeb, "A picture on the web: MetaRush downloads it into Resources, noting where it came from.", true},
		{"https://example.com/pricing", resources.LinkAmbiguous, models.PlatformWeb, "This could be a video to import or a web page to use in graphics.", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			lr := RouteLink(c.in)
			if lr.Input != c.in || lr.Route != c.route || lr.Platform != c.platform || lr.Ready != c.ready || lr.Message != c.message {
				t.Fatalf("got route %q platform %q ready %v message %q", lr.Route, lr.Platform, lr.Ready, lr.Message)
			}
			if c.route != "invalid" && lr.URL == "" {
				t.Fatal("a routed link carries the address it will use")
			}
		})
	}
}

// Tests take snapshots of pages served on this computer; only then is a local
// address a page (never in normal use, where it is refused above).
func TestLocalPagesOnlyWhenTestsAllowThem(t *testing.T) {
	t.Setenv("METARUSH_PAGES_ALLOW_LOCAL", "1")
	page := RouteLink("  http://127.0.0.1:5126/  ")
	if page.Route != resources.LinkPage || !page.Ready || page.Platform != models.PlatformWeb || page.URL != "http://127.0.0.1:5126/" ||
		!strings.HasPrefix(page.Message, "A web page: MetaRush takes a dated snapshot") {
		t.Fatalf("local page: %+v", page)
	}
	pic := RouteLink("http://127.0.0.1:5126/logo.png")
	if pic.Route != resources.LinkPage || !strings.HasPrefix(pic.Message, "A picture on the web") {
		t.Fatalf("local picture: %+v", pic)
	}
}

// comingSoon stands in for a URL source that isn't built in this version.
type comingSoon struct{ name, route string }

func (c comingSoon) Name() string        { return c.name }
func (c comingSoon) Route() string       { return c.route }
func (c comingSoon) Match(*url.URL) bool { return true }
func (c comingSoon) Add(context.Context, *app.App, resources.URLAddIn) (*resources.URLAddOut, error) {
	return nil, resources.ErrComingSoon
}

// swapSource replaces a URL source (by name) for one test and puts the real
// one back afterwards.
func swapSource(t *testing.T, s resources.URLSource, probe string) {
	t.Helper()
	u, _ := url.Parse(probe)
	orig := resources.URLSourceFor(s.Route(), u)
	if orig == nil || orig.Name() != s.Name() {
		t.Fatalf("no %s source to replace (found %v)", s.Name(), orig)
	}
	resources.RegisterURLSource(s)
	t.Cleanup(func() { resources.RegisterURLSource(orig) })
}

// A kind of address this version can't bring in yet is recognized but not
// offered: the person is told it isn't available rather than seeing it fail.
func TestRouteLinkSaysWhenAKindIsNotAvailableYet(t *testing.T) {
	swapSource(t, comingSoon{name: "github", route: resources.LinkRepo}, "https://github.com/acme/widget")
	lr := RouteLink("https://github.com/acme/widget")
	if lr.Route != resources.LinkRepo || lr.Ready || lr.Message != resources.ComingSoonMessage(resources.LinkRepo) {
		t.Fatalf("repository: %+v", lr)
	}
	swapSource(t, comingSoon{name: "website", route: resources.LinkPage}, "https://example.com/logo.png")
	lr = RouteLink("https://example.com/logo.png")
	if lr.Route != resources.LinkPage || lr.Ready || !strings.Contains(lr.Message, "can't take snapshots of web pages yet") {
		t.Fatalf("page: %+v", lr)
	}
}

func TestFindLinksPullsAddressesOutOfPastedText(t *testing.T) {
	text := "Look at <https://example.com/a>, and (https://youtu.be/dQw4w9WgXcQ); also https://example.com/a again.\n" +
		"Not these: ftp://example.com/b www.example.com https:// \"https://\" mailto:someone@example.com"
	got := findLinks(text)
	want := []string{"https://example.com/a", "https://youtu.be/dQw4w9WgXcQ"}
	if !slices.Equal(got, want) {
		t.Fatalf("links %q, want %q", got, want)
	}
	if findLinks("") != nil || findLinks("no links here") != nil {
		t.Fatal("text without links has none")
	}
}

// The Add box says in a few words what a drop holds before anything is sent.
func TestSummariesSayWhatWasDroppedAndAdded(t *testing.T) {
	routes := []FileRoute{{Route: resources.RouteFootage}, {Route: resources.RouteResource}, {Route: resources.RouteResource}, {Route: resources.RouteUnknown}}
	if got := summarizeRoutes(routes); got != "1 video or sound file and 2 pictures, PDFs or fonts and 1 other file" {
		t.Fatalf("routes: %q", got)
	}
	if got := summarizeRoutes([]FileRoute{{Route: resources.RouteFootage}, {Route: resources.RouteFootage}}); got != "2 videos or sound files" {
		t.Fatalf("two videos: %q", got)
	}
	if got := summarizeRoutes(nil); got != "" {
		t.Fatalf("nothing: %q", got)
	}

	out := &AddPathsOut{Files: []AddedFile{{Status: "ingested"}, {Status: "resumed"}, {Status: resources.StatusAdded}, {Status: resources.StatusVersion},
		{Status: resources.StatusSame}, {Status: "deduped"}, {Status: "duplicate"}, {Status: "failed"}, {Status: "skipped"}},
		Folders: []LinkedFolderOut{{}}}
	if got := summarizeAdded(out); got != "2 clips added, 2 resources added, 3 files were already there, 1 folder linked, 1 file couldn't be added." {
		t.Fatalf("added: %q", got)
	}
	one := &AddPathsOut{Files: []AddedFile{{Status: "ingested"}, {Status: resources.StatusAdded}, {Status: resources.StatusSame}, {Status: "failed"}, {Status: "failed"}},
		Folders: []LinkedFolderOut{{}, {}}}
	if got := summarizeAdded(one); got != "1 clip added, 1 resource added, 1 file was already there, 2 folders linked, 2 files couldn't be added." {
		t.Fatalf("one of each: %q", got)
	}
	if got := summarizeAdded(&AddPathsOut{Files: []AddedFile{{Status: "skipped"}}}); got != "Nothing to add." {
		t.Fatalf("nothing: %q", got)
	}
}

func TestSmallWordHelpers(t *testing.T) {
	if upper("") != "" || upper("links can't be used") != "Links can't be used" {
		t.Fatal("upper")
	}
	if count(1, "link", "links") != "1 link" || count(0, "link", "links") != "0 links" || count(3, "link", "links") != "3 links" {
		t.Fatal("count")
	}
	if plural(1, "clip") != "1 clip" || plural(2, "clip") != "2 clips" {
		t.Fatal("plural")
	}
	for p, want := range map[string]bool{"/logo.PNG": true, "/deck.pdf": true, "/photo.heic": true, "/notes.txt": false, "/": false} {
		if pictureLink(p) != want {
			t.Errorf("pictureLink(%q) = %v", p, !want)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	if got := expandHome("~/Movies/in"); got != filepath.Join(home, "Movies", "in") {
		t.Fatalf("expandHome: %q", got)
	}
	for _, p := range []string{"/abs/path", "relative", "~other/x", "~"} {
		if expandHome(p) != p {
			t.Errorf("expandHome(%q) changed it", p)
		}
	}
	if string(mustJSON(map[string]any{"ref": "logo"})) != `{"ref":"logo"}` {
		t.Fatal("mustJSON")
	}
}
