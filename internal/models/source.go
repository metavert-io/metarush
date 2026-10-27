package models

import (
	"strings"
	"time"
)

// Platforms a link import can come from. "web" is any other site the
// downloader supports (it is still recorded the same way).
const (
	PlatformYouTube = "youtube"
	PlatformTikTok  = "tiktok"
	PlatformX       = "x"
	PlatformWeb     = "web"
	// PlatformDrive: a file fetched from a Google Drive folder through the
	// person's own rclone (D220); VideoID is its Drive ID.
	PlatformDrive = "drive"
	// PlatformDropbox: a file fetched from a Dropbox folder through the
	// person's own rclone (D306); VideoID is its Dropbox ID (id:…).
	PlatformDropbox = "dropbox"
)

// How an imported file was obtained.
const (
	SourceDownload  = "download"  // MetaRush's downloader (yt-dlp) fetched the original file
	SourceBrowser   = "browser"   // the browser extension fetched the file in the person's browser
	SourceRecording = "recording" // the browser extension recorded the video while it played
)

// ViaExtension marks uploads and imports that came from the "Send to MetaRush"
// browser extension (a paired browser with an upload-only token).
const ViaExtension = "extension"

// AssetSource is where an imported clip came from: the original post on a
// video site and how it was fetched. It is provenance, kept with the clip for
// credits and reference; MetaRush asks no rights question at import (D65).
type AssetSource struct {
	Platform string `bson:"platform" json:"platform" jsonschema:"enum=youtube,enum=tiktok,enum=x,enum=web,enum=generated,enum=remotion,enum=drive,enum=dropbox,enum=ability"`
	// URL is the canonical address of the original post.
	URL     string `bson:"url" json:"url"`
	VideoID string `bson:"videoId,omitempty" json:"videoId,omitempty" jsonschema:"description=The site's ID for the video (YouTube video ID\\, TikTok video ID\\, X post or media ID)."`
	// Uploader is the channel or account's display name; Handle is its @name.
	Uploader   string `bson:"uploader,omitempty" json:"uploader,omitempty"`
	Handle     string `bson:"handle,omitempty" json:"handle,omitempty" jsonschema:"description=The account's handle with its @ (YouTube @handle\\, TikTok @name\\, X @name)."`
	ChannelURL string `bson:"channelUrl,omitempty" json:"channelUrl,omitempty"`
	// UploadDate is the day the original was posted (YYYY-MM-DD), when the site says.
	UploadDate  string `bson:"uploadDate,omitempty" json:"uploadDate,omitempty"`
	Title       string `bson:"title,omitempty" json:"title,omitempty" jsonschema:"description=The original title."`
	Description string `bson:"description,omitempty" json:"description,omitempty" jsonschema:"description=The original description\\, shortened."`
	// License is the license the site states for the video (e.g. YouTube's
	// Creative Commons Attribution license); empty when none is stated.
	License      string    `bson:"license,omitempty" json:"license,omitempty"`
	Duration     float64   `bson:"duration,omitempty" json:"duration,omitempty"`
	DownloadedAt time.Time `bson:"downloadedAt" json:"downloadedAt"`
	// Checksum is the service's own checksum of the file when it was fetched
	// from a cloud folder ("dropbox:…", "sha256:…"; D306), so a later look can
	// tell the file there is still the same.
	Checksum string `bson:"checksum,omitempty" json:"checksum,omitempty"`
	// Tool and ToolVersion name what fetched it: yt-dlp and its version, or the
	// MetaRush browser extension and its version.
	Tool        string `bson:"tool" json:"tool"`
	ToolVersion string `bson:"toolVersion,omitempty" json:"toolVersion,omitempty"`
	Method      string `bson:"method,omitempty" json:"method,omitempty" jsonschema:"enum=download,enum=browser,enum=recording,enum=generated,enum=render,enum=existing,enum=ability"`
	// SignedIn is true when the download used the person's browser sign-in
	// (yt-dlp read the browser's cookies itself; MetaRush never copies them).
	SignedIn bool `bson:"signedIn,omitempty" json:"signedIn,omitempty"`
	// Generated is set on a clip a generative-video provider made (platform
	// "generated"; D125): the provider, the model and the generation record.
	Generated *GeneratedSource `bson:"generated,omitempty" json:"generated,omitempty"`
	// Remotion is set on a clip made from a Remotion source (platform
	// "remotion"; D135): the source, its version, the composition and props
	// MetaRush rendered, or the out/ video the project already had.
	Remotion *RemotionClipSource `bson:"remotion,omitempty" json:"remotion,omitempty"`
	// MadeWith is set on a clip an ability made (platform "ability"; D234):
	// the ability and its version, the run and its inputs.
	MadeWith *AbilityMadeWith `bson:"madeWith,omitempty" json:"madeWith,omitempty"`
}

// AssetCaptions is a captions file kept with a clip (D285): its language,
// whether a machine translated it, how it was made and its stored file.
type AssetCaptions struct {
	Language   string `bson:"language" json:"language"`
	Format     string `bson:"format" json:"format" jsonschema:"enum=srt,enum=vtt"`
	Lines      int    `bson:"lines" json:"lines"`
	Translated bool   `bson:"translated,omitempty" json:"translated,omitempty" jsonschema:"description=A machine translated it."`
	// Model is what made it, as the run said (whisper.cpp medium, multilingual).
	Model      string           `bson:"model,omitempty" json:"model,omitempty"`
	SHA256     string           `bson:"sha256" json:"sha256"`
	StorageKey string           `bson:"storageKey" json:"storageKey"`
	Size       int64            `bson:"size" json:"size"`
	MadeWith   *AbilityMadeWith `bson:"madeWith,omitempty" json:"madeWith,omitempty"`
	At         time.Time        `bson:"at" json:"at"`
}

// IsAbility reports a clip an ability made.
func (s *AssetSource) IsAbility() bool {
	return s != nil && (s.Platform == PlatformAbility || s.MadeWith != nil)
}

// AbilityTitle names the ability that made it ("" when none did).
func (s *AssetSource) AbilityTitle() string {
	if !s.IsAbility() || s.MadeWith == nil {
		return ""
	}
	if s.MadeWith.Title != "" {
		return s.MadeWith.Title
	}
	return s.MadeWith.Name
}

// PlatformName is a platform as people say it: YouTube, TikTok, X.
func PlatformName(p string) string {
	switch p {
	case PlatformYouTube:
		return "YouTube"
	case PlatformTikTok:
		return "TikTok"
	case PlatformX:
		return "X"
	case "", PlatformWeb:
		return "the web"
	case PlatformGenerated:
		return "generative video"
	case PlatformRemotion:
		return "a Remotion project"
	case PlatformDrive:
		return "Google Drive"
	case PlatformDropbox:
		return "Dropbox"
	case PlatformAbility:
		return "an ability"
	}
	return p
}

// Who is the account the clip came from: the @handle when there is one, else
// the uploader's name.
func (s *AssetSource) Who() string {
	if s == nil {
		return ""
	}
	if s.Generated != nil {
		return s.Generated.ProviderTitle
	}
	if s.Remotion != nil {
		if s.Remotion.SourceTitle != "" {
			return s.Remotion.SourceTitle
		}
		return s.Remotion.SourceName
	}
	if h := strings.TrimSpace(s.Handle); h != "" {
		if !strings.HasPrefix(h, "@") {
			h = "@" + h
		}
		return h
	}
	return strings.TrimSpace(s.Uploader)
}

// UploadedOn formats the upload date as "12 Mar 2024" ("" when unknown).
func (s *AssetSource) UploadedOn() string {
	if s == nil || s.UploadDate == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", s.UploadDate)
	if err != nil {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// Line is the clip's provenance in one line: "From YouTube · @handle · 12 Mar 2024".
func (s *AssetSource) Line() string {
	if s == nil {
		return ""
	}
	if s.IsGenerated() {
		return s.GeneratedLine()
	}
	if s.IsRemotion() {
		return s.RemotionLine()
	}
	if s.IsAbility() {
		line := "Made with the ability “" + s.AbilityTitle() + "”"
		if s.MadeWith != nil && s.MadeWith.Licence != "" {
			line += " · " + s.MadeWith.Licence
		}
		return line
	}
	parts := []string{"From " + PlatformName(s.Platform)}
	if w := s.Who(); w != "" {
		parts = append(parts, w)
	}
	if d := s.UploadedOn(); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, " · ")
}

// Short names the source in a few words: "@handle on YouTube".
func (s *AssetSource) Short() string {
	if s == nil {
		return ""
	}
	if s.IsGenerated() {
		if s.Generated != nil && s.Generated.Kind == "presenter" {
			return "an AI presenter"
		}
		return "AI-generated footage"
	}
	if s.IsRemotion() {
		return "the Remotion project “" + s.Who() + "”"
	}
	if s.IsAbility() {
		return "the ability “" + s.AbilityTitle() + "”"
	}
	who := s.Who()
	if who == "" {
		who = "an account"
	}
	return who + " on " + PlatformName(s.Platform)
}

// Credit is the credit line for deliveries: "Includes footage from @handle on
// YouTube: https://…".
func (s *AssetSource) Credit() string {
	if s == nil {
		return ""
	}
	if s.IsGenerated() {
		c := "Includes AI-generated footage"
		if g := s.Generated; g != nil && g.ProviderTitle != "" {
			c += " made with " + g.ProviderTitle
			if g.ModelTitle != "" {
				c += " (" + g.ModelTitle + ")"
			}
		}
		return c
	}
	if s.IsRemotion() {
		return "" // the person's own project: nothing to credit
	}
	if s.IsAbility() {
		// What the ability said about the file (a stock clip's licence, the
		// credit it asks for and its page; D283).
		return s.MadeWith.CreditLine("footage")
	}
	who := s.Who()
	if who == "" {
		who = "an account"
	}
	c := "Includes footage from " + who + " on " + PlatformName(s.Platform)
	if s.URL != "" {
		c += ": " + s.URL
	}
	return c
}
