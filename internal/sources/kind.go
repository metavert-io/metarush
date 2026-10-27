// Package sources is what MetaRush says about each source (D150, D151): its
// kind, its descriptive name (suggested from MetaRush's first look, never
// replacing a person's), and its info card. The fixes themselves are in
// internal/fixes.
package sources

import (
	"context"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What a source is, for its name: a finer split than the fix kinds (a screen
// recording is offered a call's fixes, but is named as a screen recording).
const (
	whatCall   = "call"
	whatScreen = "screen"
	whatVideo  = "video"
	whatVoice  = "voice"
	whatMusic  = "music"
)

var (
	// Files call apps leave (Zoom's audio_only and per-person video files,
	// Riverside, SquadCast, Meet, Teams…) and names people give calls.
	callNameRe = regexp.MustCompile(`(?i)(\bzoom\b|zoom_\d|audio_only|^video\d+|gmt\d{8}|riverside|squadcast|\bmeet\b|\bteams\b|webex|\bskype\b|facetime|\bcall\b|\binterview call\b|\bpodcast\b)`)
	// Screen recordings: the system's and the usual recorders' names.
	screenNameRe = regexp.MustCompile(`(?i)(screen ?recording|screen_recording|screencast|screen capture|screen-capture|\bobs\b|loom|cleanshot|camtasia|screenflow)`)
	callAppRe    = regexp.MustCompile(`(?i)(zoom|teams|google meet|riverside|squadcast|webex|skype|facetime|zencastr|streamyard)`)
	screenAppRe  = regexp.MustCompile(`(?i)(screencapturekit|screen recording|\bobs\b|loom|cleanshot|camtasia|screenflow|quicktime player)`)
	musicNameRe  = regexp.MustCompile(`(?i)(\bmusic\b|\bsong\b|\btrack\b|\bbeat\b|\bbgm\b|soundtrack|instrumental|\bloop\b|\bscore\b|\bjingle\b|\bremix\b)`)
)

// Kind is the kind of source (the fixes it is offered, models.Source*) and
// what it is for its name.
func Kind(ctx context.Context, a *app.App, as *models.Asset) (kind, what string) {
	what = detectWhat(ctx, a, as)
	switch what {
	case whatCall, whatScreen:
		kind = models.SourceCall
	case whatVoice:
		kind = models.SourceVoice
	case whatMusic:
		kind = models.SourceMusic
	default:
		kind = models.SourceCamera
	}
	if f := as.Fixes; f != nil && f.KindBy == models.TitleByPerson && f.Kind != "" {
		kind = f.Kind
		switch kind {
		case models.SourceMusic:
			what = whatMusic
		case models.SourceVoice:
			what = whatVoice
		case models.SourceCall:
			if what != whatScreen {
				what = whatCall
			}
		default:
			what = whatVideo
		}
	}
	return kind, what
}

func detectWhat(ctx context.Context, a *app.App, as *models.Asset) string {
	name := as.Name + " " + strings.Join(aliasNames(as), " ")
	software := as.Media.Software
	if !as.Media.HasVideo {
		if as.Media.HasAudio && isMusic(as) {
			return whatMusic
		}
		return whatVoice
	}
	if inConversation(ctx, a, as.ID) || callNameRe.MatchString(name) || callAppRe.MatchString(software) {
		if screenNameRe.MatchString(name) {
			return whatScreen
		}
		return whatCall
	}
	if screenNameRe.MatchString(name) || screenAppRe.MatchString(software) {
		return whatScreen
	}
	return whatVideo
}

// isMusic: a recording someone recorded music rights for, tagged as music,
// carrying an artist, named like music, or with almost no words in it.
func isMusic(as *models.Asset) bool {
	if as.MadeNarration() || as.HasTag("voiceover") {
		return false
	}
	if as.Rights != nil || as.HasTag("music") || as.Media.TagArtist != "" || musicNameRe.MatchString(as.Name) {
		return true
	}
	if t := as.Transcript; t != nil && t.State == "available" && as.Media.Duration >= 20 {
		perMinute := float64(t.Words) / (as.Media.Duration / 60)
		return perMinute < 20
	}
	return false
}

func aliasNames(as *models.Asset) []string {
	out := make([]string, 0, len(as.Aliases))
	for _, al := range as.Aliases {
		out = append(out, al.Name)
	}
	return out
}

// inConversation reports whether a recording is part of a conversation (D99).
func inConversation(ctx context.Context, a *app.App, assetID string) bool {
	if a == nil || a.DB == nil {
		return false
	}
	n, err := a.DB.C(store.CConversations).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "members.assetId": assetID})
	return err == nil && n > 0
}
