package assistant

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/resources"
)

// describeLinkImport is the card for `metarush import <link>…`: a video from
// YouTube, TikTok, X or another site, downloaded on this computer and added
// like a dropped file. A link the person typed in the conversation starts by
// itself when their setting allows (D141); playlists and imports with the
// browser's sign-in always wait for a click.
func (m *Manager) describeLinkImport(ctx context.Context, call *cliCall, a *AssistantAction) {
	var links []string
	for _, x := range call.Args {
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
			links = append(links, x)
		}
	}
	if len(links) == 0 {
		incomplete(a, "Bring in a video", "Claude did not give a link.")
		return
	}
	if len(links) > 10 {
		incomplete(a, "Bring in videos", "Claude asked for more than 10 links at once.")
		return
	}
	for _, l := range links {
		u, err := url.Parse(l)
		if err != nil || u.Host == "" || u.User != nil {
			incomplete(a, "Bring in a video", "That isn't a web address MetaRush can use.")
			return
		}
		if !resources.AllowLocalPages() && resources.LocalAddress(u.Hostname()) {
			unavailable(a, "Bring in a video from "+u.Host, "That address is on this computer or a local network, so MetaRush won't download from it.")
			return
		}
	}
	prj, ok := projectFlag(call)
	if !ok {
		incomplete(a, "Bring in a video", "Claude did not name the project by its ID.")
		return
	}
	if hasBool(call, "dry-run") {
		incomplete(a, "Look up a video", "A look-up changes nothing; it isn't a card.")
		return
	}
	in := linkimport.ImportIn{URLs: links, ProjectID: prj, BrowserSignIn: strings.ToLower(strings.TrimSpace(call.Flags["browser-signin"])), Playlist: intFlag(call, "playlist")}
	if in.BrowserSignIn != "" && !linkimport.ValidBrowser(in.BrowserSignIn) {
		incomplete(a, "Bring in a video", "The browser sign-in can come from Chrome, Safari, Firefox, Brave or Edge.")
		return
	}
	host := func(l string) string {
		u, _ := url.Parse(l)
		return strings.TrimPrefix(u.Host, "www.")
	}
	if len(links) == 1 {
		a.Title = "Bring in the video from " + host(links[0])
	} else {
		a.Title = "Bring in " + strconv.Itoa(len(links)) + " videos from their links"
	}
	a.Details = []string{"Downloaded on this computer, then added like a file you dropped in: preview, words and voices follow"}
	if in.Playlist > 0 {
		a.Details = append(a.Details, "The first "+pluralize(in.Playlist, "video")+" of the playlist")
	}
	if in.BrowserSignIn != "" {
		a.Details = append(a.Details, "Uses your "+upperFirst(in.BrowserSignIn)+" sign-in for this download only")
	}
	if prj != "" {
		a.Details = append(a.Details, "Into "+m.nameOf(ctx, prj))
	} else {
		a.Details = append(a.Details, "Into the Inbox")
	}
	setInput(a, "ingest.url", in)
}

// savesVersion reports whether a card's op saves a new version of the cut, so
// its card offers "Save and render a review copy" (D144).
func savesVersion(op string) bool {
	return op == "edit.apply" || op == "cut.apply" || op == "cut.transition" || op == "cut.captions" || op == "footage.short" || op == "cut.say.place" || op == "cut.say.undo" || isCutEditOp(op)
}
