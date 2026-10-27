package models

import (
	"strings"
	"time"
)

// Resource kinds (D79). A resource is something a cut's graphics use that is
// not footage: a logo, a photo, a screenshot, a page of a PDF, a font.
const (
	ResourceImage = "image" // PNG, JPEG, WebP (HEIC photos are converted to JPEG on import)
	ResourceSVG   = "svg"   // vector images, sanitized on import (no scripts, no outside references)
	ResourcePDF   = "pdf"   // each page is also kept as a picture (poppler, optional)
	ResourceFont  = "font"  // TTF, OTF, WOFF2
	// ResourceRepo is a GitHub repository pinned at a commit (D104): each
	// version is a filtered snapshot of its files (a tar.gz in the media
	// store) with an overview. Claude reads it through a read-only checkout;
	// MetaRush never runs anything from it. Code slides, diagrams and stats
	// cards are made from it (generated pictures that name it as their source).
	ResourceRepo = "repo"
	// ResourceWebsite is a dated snapshot of a web page (D105): its file is
	// the desktop screenshot, and its page pictures are the desktop, phone and
	// full-page screenshots (name#1, #2, #3), with the readable text, title,
	// theme colours, preview picture and icon.
	ResourceWebsite = "website"
)

// Website snapshot pictures (ResourcePage.Label), in page order.
const (
	ShotDesktop = "desktop"
	ShotMobile  = "mobile"
	ShotFull    = "full"
)

// IsPicture reports whether a resource kind draws as a picture (graphics can
// show it; a font and a repository can't).
func IsPicture(kind string) bool {
	switch kind {
	case ResourceImage, ResourceSVG, ResourcePDF, ResourceWebsite:
		return true
	}
	return false
}

// Resource scopes: a project's own, or the whole workspace's library (every
// project sees it; "Promote to workspace").
const (
	ScopeProject   = "project"
	ScopeWorkspace = "workspace"
)

// Resource provenance kinds: where a version came from.
const (
	FromFile      = "file"      // a file someone added (dropped, chosen, `metarush resources add`)
	FromFolder    = "folder"    // a linked folder (kept up to date while it is linked)
	FromURL       = "url"       // a web page or image address (part 2: website snapshots)
	FromRepo      = "repo"      // a GitHub repository at a commit (part 2)
	FromProject   = "project"   // copied in from another project
	FromGenerated = "generated" // made by MetaRush from other resources (lineage says from what)
	FromDrive     = "drive"     // a Google Drive folder, fetched through the person's own rclone (D220)
	FromDropbox   = "dropbox"   // a Dropbox folder, fetched through the person's own rclone (D306)
)

// Resource is an image, logo, PDF or font a project — or the whole workspace —
// uses in its graphics: a stable name plus content-addressed versions. Nothing
// is ever overwritten: a changed file is a new version, and cuts pin the exact
// version they use (spec.resources), so they keep rendering the same picture
// until someone updates them ("Update to latest").
type Resource struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// Name is the stable short name cuts and cut sheets use ("logo",
	// "team-photo"): lowercase letters, digits, - and _, unique within its scope.
	Name string `bson:"name" json:"name"`
	// Title is the display name people see ("Acme logo").
	Title string `bson:"title" json:"title"`
	Kind  string `bson:"kind" json:"kind" jsonschema:"enum=image,enum=svg,enum=pdf,enum=font,enum=repo,enum=website"`
	Scope string `bson:"scope" json:"scope" jsonschema:"enum=project,enum=workspace"`
	// ProjectID is the project a project-scoped resource belongs to.
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Versions, oldest first. Each is content-addressed (SHA-256).
	Versions []ResourceVersion `bson:"versions" json:"versions"`
	// Current is the version new uses get (the latest unless a person picked one).
	Current string `bson:"current" json:"current" jsonschema:"description=SHA-256 of the version new uses pick."`
	// Tags are people's own; AutoTags are what MetaRush noticed (never mixed, so
	// a suggestion never overwrites a person's word).
	Tags        []string      `bson:"tags,omitempty" json:"tags,omitempty"`
	AutoTags    []ResourceTag `bson:"autoTags,omitempty" json:"autoTags,omitempty"`
	Description string        `bson:"description,omitempty" json:"description,omitempty"`
	// License is a person's note about the licence (fonts, stock photos, logos
	// used with permission). Web- and repo-sourced images without one get an
	// informational QA note; nothing is ever blocked for it.
	License string `bson:"license,omitempty" json:"license,omitempty"`
	// Folder is the linked folder this resource comes from (kept up to date).
	Folder *ResourceFolderRef `bson:"folder,omitempty" json:"folder,omitempty"`
	// RepoFile is the file in a repository a picture was kept from (D216): the
	// same file at a newer commit is a new version of this resource.
	RepoFile *ResourceRepoFile `bson:"repoFile,omitempty" json:"repoFile,omitempty"`
	// DriveFile is the Google Drive file a picture was fetched from (D220):
	// the same file with new contents is a new version of this resource.
	DriveFile *ResourceDriveFile `bson:"driveFile,omitempty" json:"driveFile,omitempty"`
	// PicturePaths are a repository's folders (or files) whose pictures
	// MetaRush keeps (D216): each PNG, JPEG, WebP, GIF or SVG file under them is
	// a picture resource of its own, pinned to the commit and refreshed with it.
	PicturePaths []string `bson:"picturePaths,omitempty" json:"picturePaths,omitempty" jsonschema:"description=A repository's folders or files whose pictures are kept (. is the whole repository)."`
	// Missing: the file is no longer in its linked folder (or, for a picture
	// kept from a repository, in the repository's newest commit). The resource
	// and its versions stay (cuts that use it keep rendering).
	Missing      bool       `bson:"missing,omitempty" json:"missing,omitempty"`
	MissingSince *time.Time `bson:"missingSince,omitempty" json:"missingSince,omitempty"`
	// Removed hides it from lists and pickers. Its bytes and versions stay, so
	// cuts that pin it still render; "remove" never deletes media.
	Removed   bool       `bson:"removed,omitempty" json:"removed,omitempty"`
	RemovedAt *time.Time `bson:"removedAt,omitempty" json:"removedAt,omitempty"`
	RemovedBy *Actor     `bson:"removedBy,omitempty" json:"removedBy,omitempty"`
	// Fixes are a picture's fixes (D152): what MetaRush measured, what a
	// person ticked, and the fixed version made from the version in From.
	Fixes *SourceFixes `bson:"fixes,omitempty" json:"fixes,omitempty"`
	// PromotedFrom is the project a workspace resource was promoted from.
	PromotedFrom string    `bson:"promotedFrom,omitempty" json:"promotedFrom,omitempty"`
	CreatedAt    time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy    Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt    time.Time `bson:"updatedAt" json:"updatedAt"`
}

// ResourceVersion is one exact file of a resource.
type ResourceVersion struct {
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
	MIME   string `bson:"mime" json:"mime"`
	// Ext is the file extension it is served with (png, jpg, webp, svg, pdf, ttf, otf, woff2).
	Ext        string `bson:"ext" json:"ext"`
	StorageKey string `bson:"storageKey" json:"-"`
	// Stored: the bytes are in the media store, so every machine can use them.
	// A large file from a linked folder waits on the machines in Holders until
	// a cut first uses it (resources.uploadSmallMB, per folder Upload).
	Stored  bool     `bson:"stored" json:"stored"`
	Holders []string `bson:"holders,omitempty" json:"holders,omitempty" jsonschema:"description=Installations that have the file locally (a linked folder) while it is not stored yet."`
	// Pictures: size, transparency and the dominant colours (most first).
	Width    int      `bson:"width,omitempty" json:"width,omitempty"`
	Height   int      `bson:"height,omitempty" json:"height,omitempty"`
	HasAlpha bool     `bson:"hasAlpha,omitempty" json:"hasAlpha,omitempty"`
	Colors   []string `bson:"colors,omitempty" json:"colors,omitempty" jsonschema:"description=Dominant colours as #RRGGBB\\, most used first."`
	// Thumbnail is a small PNG for lists (SVGs are shown as they are).
	Thumbnail *ResourceObject `bson:"thumbnail,omitempty" json:"thumbnail,omitempty"`
	// Pages are a PDF's pages as pictures (page 1 first), when poppler is installed.
	Pages     []ResourcePage `bson:"pages,omitempty" json:"pages,omitempty"`
	PageCount int            `bson:"pageCount,omitempty" json:"pageCount,omitempty"`
	// Fonts: the family and style the file declares.
	Family string `bson:"family,omitempty" json:"family,omitempty"`
	Style  string `bson:"style,omitempty" json:"style,omitempty"`
	// Text is text found in the picture (OCR, by the on-device visual index), a
	// PDF's page pictures, or a website snapshot's readable text. Search reads it.
	Text string `bson:"text,omitempty" json:"text,omitempty"`
	// Repo describes a repository version (kind repo): the commit it is pinned
	// to and an overview of what is in it.
	Repo *RepoSnapshot `bson:"repo,omitempty" json:"repo,omitempty"`
	// Site describes a website snapshot (kind website): the page, when it was
	// taken, and what it said about itself.
	Site *SiteSnapshot `bson:"site,omitempty" json:"site,omitempty"`
	// OriginalSHA256 is the file as it arrived when MetaRush changed it on the
	// way in (a HEIC photo converted, an SVG cleaned); Changes say what changed.
	OriginalSHA256 string         `bson:"originalSha256,omitempty" json:"originalSha256,omitempty"`
	Changes        []string       `bson:"changes,omitempty" json:"changes,omitempty"`
	Source         ResourceSource `bson:"source" json:"source"`
	CreatedAt      time.Time      `bson:"createdAt" json:"createdAt"`
	CreatedBy      Actor          `bson:"createdBy" json:"createdBy"`
}

// ResourceObject is a derived file in the media store (a thumbnail).
type ResourceObject struct {
	SHA256     string `bson:"sha256" json:"sha256"`
	Size       int64  `bson:"size" json:"size"`
	MIME       string `bson:"mime" json:"mime"`
	StorageKey string `bson:"storageKey" json:"-"`
	Width      int    `bson:"width,omitempty" json:"width,omitempty"`
	Height     int    `bson:"height,omitempty" json:"height,omitempty"`
}

// ResourcePage is one page of a PDF as a picture, or one screenshot of a
// website snapshot (Label: desktop, mobile, full).
type ResourcePage struct {
	Number     int    `bson:"number" json:"number" jsonschema:"description=1 for the first page."`
	SHA256     string `bson:"sha256" json:"sha256"`
	Size       int64  `bson:"size" json:"size"`
	StorageKey string `bson:"storageKey" json:"-"`
	Width      int    `bson:"width" json:"width"`
	Height     int    `bson:"height" json:"height"`
	Label      string `bson:"label,omitempty" json:"label,omitempty" jsonschema:"description=A website snapshot's screenshot: desktop\\, mobile or full (the whole page)."`
}

// RepoSnapshot is one version of a repository: the commit it is pinned to,
// what the snapshot holds and what MetaRush learned about the project. The
// snapshot itself is the version's file (a tar.gz of the files kept).
type RepoSnapshot struct {
	Host  string `bson:"host" json:"host" jsonschema:"description=github.com"`
	Owner string `bson:"owner" json:"owner"`
	Name  string `bson:"name" json:"name"`
	// Ref is the branch, tag or commit asked for (empty: the default branch).
	Ref         string    `bson:"ref,omitempty" json:"ref,omitempty"`
	Commit      string    `bson:"commit" json:"commit" jsonschema:"description=The full commit SHA this version is pinned to."`
	CommittedAt time.Time `bson:"committedAt,omitempty" json:"committedAt,omitempty"`
	Subject     string    `bson:"subject,omitempty" json:"subject,omitempty" jsonschema:"description=The commit's first line."`
	// Description, DefaultBranch, Stars, Private and License come from GitHub
	// (gh api) when it answers; License can also come from a LICENSE file.
	Description   string   `bson:"description,omitempty" json:"description,omitempty"`
	DefaultBranch string   `bson:"defaultBranch,omitempty" json:"defaultBranch,omitempty"`
	Stars         int      `bson:"stars,omitempty" json:"stars,omitempty"`
	Forks         int      `bson:"forks,omitempty" json:"forks,omitempty"`
	Private       bool     `bson:"private,omitempty" json:"private,omitempty"`
	License       string   `bson:"license,omitempty" json:"license,omitempty" jsonschema:"description=The licence the repository states (SPDX ID when known)."`
	Topics        []string `bson:"topics,omitempty" json:"topics,omitempty"`
	// Files and Bytes count what the snapshot holds; Skipped what it left out
	// (binaries, large files, past the size cap), with SkippedWhy in words.
	Files      int      `bson:"files" json:"files"`
	Bytes      int64    `bson:"bytes" json:"bytes"`
	Skipped    int      `bson:"skipped,omitempty" json:"skipped,omitempty"`
	SkippedWhy []string `bson:"skippedWhy,omitempty" json:"skippedWhy,omitempty"`
	// Languages by size of the files kept (GitHub's own figures when gh answers).
	Languages    []RepoLanguage    `bson:"languages,omitempty" json:"languages,omitempty"`
	Contributors []RepoContributor `bson:"contributors,omitempty" json:"contributors,omitempty"`
	Recent       []RepoCommit      `bson:"recent,omitempty" json:"recent,omitempty" jsonschema:"description=The latest commits up to the pinned one\\, newest first."`
	// Readme is the start of the README (plain text, for a quick look; the
	// whole file is in the snapshot).
	Readme string `bson:"readme,omitempty" json:"readme,omitempty"`
	// Overview is the full overview (file tree, README, languages, history) as
	// JSON in the media store.
	Overview *ResourceObject `bson:"overview,omitempty" json:"overview,omitempty"`
	// History says where the history and contributors came from: gh (GitHub's
	// API, with the person's own gh sign-in) or git (the commits fetched).
	History string `bson:"history,omitempty" json:"history,omitempty" jsonschema:"enum=gh,enum=git,enum="`
	// PicturePaths are the paths this version kept pictures from (D216);
	// Pictures what it kept, in path order — each a picture resource of its own
	// at the version made from this commit; PicturesSkipped counts the files
	// under those paths it left out, PicturesSkippedWhy says why in words and
	// PicturesLeftOut lists them (the first 100).
	PicturePaths       []string      `bson:"picturePaths,omitempty" json:"picturePaths,omitempty"`
	Pictures           []RepoPicture `bson:"pictures,omitempty" json:"pictures,omitempty"`
	PicturesSkipped    int           `bson:"picturesSkipped,omitempty" json:"picturesSkipped,omitempty"`
	PicturesSkippedWhy []string      `bson:"picturesSkippedWhy,omitempty" json:"picturesSkippedWhy,omitempty"`
	PicturesLeftOut    []RepoLeftOut `bson:"picturesLeftOut,omitempty" json:"picturesLeftOut,omitempty"`
}

// RepoPicture is one picture a repository version kept (D216): the file, and
// the picture resource and version it is.
type RepoPicture struct {
	// Path is the file in the repository ("art/characters/alice.png").
	Path       string `bson:"path" json:"path"`
	ResourceID string `bson:"resourceId" json:"resourceId"`
	// SHA256 is the picture's version made from this commit (an SVG's after cleaning).
	SHA256 string `bson:"sha256" json:"sha256"`
	Bytes  int64  `bson:"bytes" json:"bytes"`
}

// RepoLeftOut is a file under a repository's picture paths that wasn't kept, and why.
type RepoLeftOut struct {
	Path string `bson:"path" json:"path"`
	Why  string `bson:"why" json:"why"`
}

// ResourceRepoFile ties a picture to the repository file it was kept from (D216).
type ResourceRepoFile struct {
	// RepoID is the repository resource; Path the file in the repository.
	RepoID string `bson:"repoId" json:"repoId"`
	Path   string `bson:"path" json:"path"`
}

// ResourceDriveFile ties a picture to the Google Drive file it was fetched
// from (D220): its Drive ID (the same on every computer), the folder as the
// person named it (rclone's remote and path) and its name there, and the
// checksum Google gave for the version fetched last.
type ResourceDriveFile struct {
	ID       string `bson:"id" json:"id"`
	Folder   string `bson:"folder" json:"folder" jsonschema:"description=The Drive folder as rclone names it (remote:path)."`
	Name     string `bson:"name" json:"name"`
	Checksum string `bson:"checksum,omitempty" json:"checksum,omitempty" jsonschema:"description=Google's checksum of the version fetched last (sha256:… or md5:…)."`
}

// RepoLanguage is one language's share of a repository.
type RepoLanguage struct {
	Name  string  `bson:"name" json:"name"`
	Bytes int64   `bson:"bytes" json:"bytes"`
	Share float64 `bson:"share" json:"share" jsonschema:"description=0–1 of the code."`
}

// RepoContributor is someone who committed to a repository.
type RepoContributor struct {
	Login   string `bson:"login,omitempty" json:"login,omitempty"`
	Name    string `bson:"name,omitempty" json:"name,omitempty"`
	Commits int    `bson:"commits" json:"commits"`
}

// RepoCommit is one commit in a repository's recent history.
type RepoCommit struct {
	SHA     string    `bson:"sha" json:"sha"`
	Subject string    `bson:"subject" json:"subject"`
	Author  string    `bson:"author,omitempty" json:"author,omitempty"`
	At      time.Time `bson:"at" json:"at"`
}

// SiteSnapshot is a dated snapshot of one web page.
type SiteSnapshot struct {
	// URL is the page as it was reached (after redirects); Requested what was asked for.
	URL       string `bson:"url" json:"url"`
	Requested string `bson:"requested,omitempty" json:"requested,omitempty"`
	Site      string `bson:"site" json:"site" jsonschema:"description=The site's name (example.com)."`
	Title     string `bson:"title,omitempty" json:"title,omitempty"`
	// Description, SiteName and ThemeColors are what the page says about itself.
	Description string   `bson:"description,omitempty" json:"description,omitempty"`
	SiteName    string   `bson:"siteName,omitempty" json:"siteName,omitempty"`
	ThemeColors []string `bson:"themeColors,omitempty" json:"themeColors,omitempty" jsonschema:"description=#RRGGBB: the page's theme colour first\\, then the screenshot's main colours."`
	Lang        string   `bson:"lang,omitempty" json:"lang,omitempty"`
	// Preview is the page's preview picture (og:image) and Icon its icon, when it has them.
	Preview *ResourceObject `bson:"preview,omitempty" json:"preview,omitempty"`
	Icon    *ResourceObject `bson:"icon,omitempty" json:"icon,omitempty"`
	// TakenAt is when the snapshot was taken; Via how: fetch (MetaRush's own
	// browser, signed out) or extension (the person's browser, as they saw it).
	TakenAt time.Time `bson:"takenAt" json:"takenAt"`
	Via     string    `bson:"via" json:"via" jsonschema:"enum=fetch,enum=extension"`
	// SignedIn: taken in the person's own browser, so it may show what only they can see.
	SignedIn bool `bson:"signedIn,omitempty" json:"signedIn,omitempty"`
	// From is the page a same-site link was followed from (snapshots at depth 1).
	From      string `bson:"from,omitempty" json:"from,omitempty"`
	TextChars int    `bson:"textChars,omitempty" json:"textChars,omitempty"`
	// TextObject holds the whole readable text when it is longer than Version.Text keeps.
	TextObject *ResourceObject `bson:"textObject,omitempty" json:"textObject,omitempty"`
	// Brand is what the snapshot read about the site's brand (D165): the logos
	// and icons it kept as pictures, its colours and its fonts. Only the first
	// page of a snapshot has it.
	Brand *SiteBrand `bson:"brand,omitempty" json:"brand,omitempty"`
}

// SiteBrand is a website's logos, colours and fonts as a page snapshot found
// them (D165). brand.suggest makes a brand kit from it.
type SiteBrand struct {
	// Logos are the pictures kept as resources of the page's scope, most
	// logo-like first ("acme-com-logo", "acme-com-logo-2", "acme-com-icon").
	Logos []SiteLogo `bson:"logos,omitempty" json:"logos,omitempty"`
	// Colors are the page's colours with where each came from, brand colours first.
	Colors []SiteColor `bson:"colors,omitempty" json:"colors,omitempty"`
	// Fonts are the families the page uses or loads; a Google font's file is kept as a font resource.
	Fonts []SiteFont `bson:"fonts,omitempty" json:"fonts,omitempty"`
}

// SiteLogo is a logo or icon a snapshot kept as a picture resource.
type SiteLogo struct {
	ResourceID string `bson:"resourceId" json:"resourceId"`
	Name       string `bson:"name" json:"name" jsonschema:"description=The picture's short name (acme-com-logo)."`
	// SHA256 is the version the snapshot kept (the resource may have newer ones).
	SHA256 string `bson:"sha256,omitempty" json:"sha256,omitempty"`
	// Kind is where the page has it: svg (drawn in the page), img (a picture),
	// background (a CSS picture), touch-icon, icon, mask-icon (the page's icons)
	// or preview (its preview picture, og:image).
	Kind  string  `bson:"kind" json:"kind" jsonschema:"enum=svg,enum=img,enum=background,enum=touch-icon,enum=icon,enum=mask-icon,enum=preview"`
	Score float64 `bson:"score" json:"score" jsonschema:"description=How logo-like it is (higher is more)."`
	// URL is the picture's own address (empty for one drawn in the page).
	URL string `bson:"url,omitempty" json:"url,omitempty"`
	// Hints say why it looks like a logo, in a few words ("says logo", "links home", "in the header").
	Hints []string `bson:"hints,omitempty" json:"hints,omitempty"`
	// Backdrop is the colour behind it on the page (#RRGGBB), when known.
	Backdrop string `bson:"backdrop,omitempty" json:"backdrop,omitempty"`
}

// SiteColor is one colour a page uses.
type SiteColor struct {
	Hex string `bson:"hex" json:"hex" jsonschema:"pattern=^#[0-9A-F]{6}$"`
	// From says where it came from: theme-color, css:<custom property>, logo,
	// header, button, section, link, header-text, background (the page's) or
	// text (the page's).
	From   string  `bson:"from" json:"from"`
	Weight float64 `bson:"weight" json:"weight" jsonschema:"description=How much the page uses it (larger is more)."`
}

// SiteFont is a font family a page uses.
type SiteFont struct {
	Family string `bson:"family" json:"family"`
	// From: h1, h2, body, nav (the families those are written in), google (a
	// Google Fonts link) or loaded (a font the page loaded).
	From string `bson:"from" json:"from"`
	// Google: the family is on Google Fonts (open licence); a site's own font is only named.
	Google bool `bson:"google,omitempty" json:"google,omitempty"`
	// ResourceID is the font file kept for it (a Google font's regular weight).
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
}

// ResourceMadeWith is how MetaRush made a generated resource (D106) — "How it
// was made" — so it can make it again from a newer commit ("refresh").
type ResourceMadeWith struct {
	// Kind: code (a code slide), diagram (Mermaid or D2), stats (a repository
	// card), ai (a picture an AI service made, D226), ability (D234).
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=code,enum=diagram,enum=stats,enum=ai,enum=ability"`
	// AI pictures: the generation record that says how it was made, the
	// service and model, and the words it was made from.
	GenerationID  string `bson:"generationId,omitempty" json:"generationId,omitempty"`
	Provider      string `bson:"provider,omitempty" json:"provider,omitempty"`
	ProviderTitle string `bson:"providerTitle,omitempty" json:"providerTitle,omitempty"`
	Model         string `bson:"model,omitempty" json:"model,omitempty"`
	Prompt        string `bson:"prompt,omitempty" json:"prompt,omitempty"`
	Watermark     string `bson:"watermark,omitempty" json:"watermark,omitempty"`
	// Likeness: it shows a real person's likeness, made with their consent (D227).
	Likeness bool `bson:"likeness,omitempty" json:"likeness,omitempty"`
	// Edit: an AI model changed a person's own picture without making
	// anything new (its background taken away), so it isn't marked as
	// AI-generated (D279).
	Edit bool `bson:"edit,omitempty" json:"edit,omitempty"`
	// Ability is set when an ability made it (kind ability, D234).
	Ability *AbilityMadeWith `bson:"ability,omitempty" json:"ability,omitempty"`
	// RepoID and RepoSHA are the repository version it was made from.
	RepoID  string `bson:"repoId,omitempty" json:"repoId,omitempty"`
	RepoSHA string `bson:"repoSha,omitempty" json:"repoSha,omitempty"`
	// Code slides: the file, the lines (first and last, 1-based), the
	// language, the lines to highlight ("12-14,20") and the code itself (to
	// find it again after the file changes).
	File      string `bson:"file,omitempty" json:"file,omitempty"`
	From      int    `bson:"from,omitempty" json:"from,omitempty"`
	To        int    `bson:"to,omitempty" json:"to,omitempty"`
	Language  string `bson:"language,omitempty" json:"language,omitempty"`
	Highlight string `bson:"highlight,omitempty" json:"highlight,omitempty"`
	Snippet   string `bson:"snippet,omitempty" json:"snippet,omitempty"`
	// Diagrams: the format and the source Claude (or a person) wrote.
	Format string `bson:"format,omitempty" json:"format,omitempty" jsonschema:"enum=mermaid,enum=d2,enum="`
	Source string `bson:"source,omitempty" json:"source,omitempty"`
	// Theme is dark or light (from the brand kit's background unless asked).
	Theme string `bson:"theme,omitempty" json:"theme,omitempty" jsonschema:"enum=dark,enum=light,enum="`
	// Caption is the header line drawn on a code slide or card.
	Caption string `bson:"caption,omitempty" json:"caption,omitempty"`
}

// ResourceTag is a tag MetaRush suggested, with where it came from.
type ResourceTag struct {
	Tag string `bson:"tag" json:"tag"`
	// Source names the tagger: filename, picture (transparency, palette, shape);
	// part 2 adds the visual index and OCR.
	Source     string  `bson:"source" json:"source"`
	Confidence float64 `bson:"confidence,omitempty" json:"confidence,omitempty"`
	Why        string  `bson:"why,omitempty" json:"why,omitempty" jsonschema:"description=Why\\, in plain words (\"the file name says logo\")."`
}

// ResourceFolderRef ties a resource to the file in a linked folder.
type ResourceFolderRef struct {
	FolderID string `bson:"folderId" json:"folderId"`
	// RelPath is the file's path inside the folder ("logos/acme.svg").
	RelPath string `bson:"relPath" json:"relPath"`
}

// ResourceSource is a version's provenance.
type ResourceSource struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=file,enum=folder,enum=url,enum=repo,enum=project,enum=generated,enum=drive,enum=dropbox"`
	// Name is the file name it arrived under.
	Name string `bson:"name,omitempty" json:"name,omitempty"`
	// Where it was on the machine that added it, with the home folder shortened
	// to ~ (provenance only; MetaRush never reads it back).
	Path    string `bson:"path,omitempty" json:"path,omitempty"`
	Machine string `bson:"machine,omitempty" json:"machine,omitempty"`
	// Folder: the linked folder and the file's place in it.
	FolderID string `bson:"folderId,omitempty" json:"folderId,omitempty"`
	RelPath  string `bson:"relPath,omitempty" json:"relPath,omitempty"`
	// URL and Repo: the address, the site, the commit, the stated licence and
	// author when the source says. A code slide made from a repository also
	// says which file and lines (File, Lines: "80-120"): its provenance is
	// repo@commit:path:lines (Provenance).
	URL     string `bson:"url,omitempty" json:"url,omitempty"`
	Site    string `bson:"site,omitempty" json:"site,omitempty"`
	Commit  string `bson:"commit,omitempty" json:"commit,omitempty"`
	License string `bson:"license,omitempty" json:"license,omitempty"`
	Author  string `bson:"author,omitempty" json:"author,omitempty"`
	File    string `bson:"file,omitempty" json:"file,omitempty" jsonschema:"description=The file inside the repository."`
	Lines   string `bson:"lines,omitempty" json:"lines,omitempty" jsonschema:"description=The lines of that file (80-120)."`
	// MadeWith is how a generated resource was made (code slide, diagram, stats card).
	MadeWith *ResourceMadeWith `bson:"madeWith,omitempty" json:"madeWith,omitempty"`
	// Project: copied in from another project's resource.
	ProjectID  string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	// Generated: what made it and from what.
	Tool    string            `bson:"tool,omitempty" json:"tool,omitempty"`
	Lineage []ResourceLineage `bson:"lineage,omitempty" json:"lineage,omitempty"`
	// Asset is the clip these bytes were first imported as (older imports of images).
	AssetID string    `bson:"assetId,omitempty" json:"assetId,omitempty"`
	At      time.Time `bson:"at" json:"at"`
}

// ResourceLineage is one input of a generated version.
type ResourceLineage struct {
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	SHA256     string `bson:"sha256" json:"sha256"`
	Step       string `bson:"step,omitempty" json:"step,omitempty" jsonschema:"description=What was done (converted from HEIC\\, page 3 of the PDF…)."`
}

// Provenance says exactly where a version came from, for credits and
// details: a repository's file at a commit ("github.com/acme/widget@1a2b3c4d:
// cmd/server/main.go:40-72"), a repository ("github.com/acme/widget@1a2b3c4d"),
// or a page on a day ("example.com/pricing, 19 Oct 2026"). Empty for files.
func (s ResourceSource) Provenance() string {
	repo := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://"), ".git")
	if s.Commit != "" && repo != "" {
		out := repo + "@" + ShortSHA(s.Commit)
		if s.File != "" {
			out += ":" + s.File
			if s.Lines != "" {
				out += ":" + s.Lines
			}
		}
		return out
	}
	if s.MadeWith != nil && s.MadeWith.Ability != nil {
		return "the ability " + s.MadeWith.Ability.Ability // name@version (D234)
	}
	if s.Kind == FromURL && s.URL != "" {
		page := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://"), "/")
		if !s.At.IsZero() {
			return page + ", " + s.At.UTC().Format("2 Jan 2006")
		}
		return page
	}
	return ""
}

// ShortSHA is the first 8 characters of a commit or content hash.
func ShortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// Version returns the version with that hash (nil when there is none).
func (r *Resource) Version(sha string) *ResourceVersion {
	for i := range r.Versions {
		if r.Versions[i].SHA256 == sha {
			return &r.Versions[i]
		}
	}
	return nil
}

// Latest is the version new uses get.
func (r *Resource) Latest() *ResourceVersion {
	if v := r.Version(r.Current); v != nil {
		return v
	}
	if n := len(r.Versions); n > 0 {
		return &r.Versions[n-1]
	}
	return nil
}

// AllTags lists a person's tags, then MetaRush's suggestions not already among them.
func (r *Resource) AllTags() []string {
	out := append([]string{}, r.Tags...)
	seen := map[string]bool{}
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range r.AutoTags {
		if !seen[t.Tag] {
			seen[t.Tag] = true
			out = append(out, t.Tag)
		}
	}
	return out
}

// Linked-folder upload policies (D81).
const (
	UploadSmall = "small"  // files up to the size limit upload when added; larger ones when a cut first uses them (default)
	UploadAll   = "all"    // everything uploads when it is added
	UploadOnUse = "on-use" // nothing uploads until a cut uses it
)

// LinkedFolder is a folder on someone's computer that a project (or the
// workspace library) keeps in step with: new and changed files become
// resources or new versions, footage becomes sources, and a file that goes
// away leaves its resource in place, marked missing. The folder's path is per
// machine and lives in that machine's config.yaml (resources.folders); this
// shared record holds what every machine needs to know.
type LinkedFolder struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// Name is the folder's own name ("Brand assets").
	Name      string `bson:"name" json:"name"`
	Scope     string `bson:"scope" json:"scope" jsonschema:"enum=project,enum=workspace"`
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Upload says when files are copied to the media store (small, all, on-use);
	// SmallMB is the size limit for "small" (default resources.uploadSmallMB, else 25).
	Upload  string `bson:"upload" json:"upload" jsonschema:"enum=small,enum=all,enum=on-use"`
	SmallMB int    `bson:"smallMb,omitempty" json:"smallMb,omitempty"`
	// Machines is the folder's state on each machine that has it linked, by
	// installation ID (published by that machine's watcher).
	Machines  map[string]LinkedFolderMachine `bson:"machines,omitempty" json:"machines,omitempty"`
	Unlinked  bool                           `bson:"unlinked,omitempty" json:"unlinked,omitempty" jsonschema:"description=No longer kept in step (its resources stay)."`
	CreatedAt time.Time                      `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor                          `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time                      `bson:"updatedAt" json:"updatedAt"`
}

// LinkedFolderMachine is a linked folder's state on one machine.
type LinkedFolderMachine struct {
	Machine string `bson:"machine" json:"machine"`
	// Display is the folder on that machine with the home folder shortened to ~.
	Display   string    `bson:"display" json:"display"`
	State     string    `bson:"state" json:"state" jsonschema:"enum=watching,enum=missing,enum=off"`
	LastScan  time.Time `bson:"lastScan" json:"lastScan"`
	Files     int       `bson:"files" json:"files"`
	Resources int       `bson:"resources" json:"resources"`
	Footage   int       `bson:"footage" json:"footage"`
	Waiting   int       `bson:"waiting" json:"waiting" jsonschema:"description=Large files that upload when a cut first uses them."`
	Pending   []string  `bson:"pending,omitempty" json:"pending,omitempty" jsonschema:"description=Files still being copied in (they are added once they stop changing)."`
	Errors    []string  `bson:"errors,omitempty" json:"errors,omitempty" jsonschema:"description=Files that couldn't be added\\, each with its latest problem (the first 10 by name); one stays until the file is added or goes away."`
	// Failed counts the files that couldn't be added; RetryAt is when the next
	// of them is tried again by itself (a file that changes is tried at once).
	Failed    int        `bson:"failed,omitempty" json:"failed,omitempty" jsonschema:"description=How many files couldn't be added (they are tried again)."`
	RetryAt   *time.Time `bson:"retryAt,omitempty" json:"retryAt,omitempty" jsonschema:"description=When the next file that couldn't be added is tried again by itself."`
	UpdatedAt time.Time  `bson:"updatedAt" json:"updatedAt"`
}

// Link kinds: what a project link can bring in (D83).
const (
	LinkFootage   = "footage"
	LinkResources = "resources"
	LinkMotion    = "motion"
	LinkCuts      = "cuts"
)

// ProjectLink lets a project use another project's things — by default
// everything: its footage, resources, approved motion graphics and cuts. They
// show up marked "from <project>", new items there show up here too, and
// nothing is copied (the same hashes are used). A link can be narrowed to a
// saved filter or to specific items. Unlinking keeps what a cut already uses
// (Kept, with those Items) and says so.
type ProjectLink struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// ProjectID is the project that uses; SourceID the project whose things it uses.
	ProjectID string `bson:"projectId" json:"projectId"`
	SourceID  string `bson:"sourceId" json:"sourceId"`
	// Only narrows the link (nil: everything). Within one field any value
	// matches; across fields all must (kinds=resources and tags=logo: the
	// resources tagged logo).
	Only *LinkFilter `bson:"only,omitempty" json:"only,omitempty"`
	// Items are specific things, added to whatever Only lets through (with
	// Only nil and Items set, only the items).
	Items []LinkItem `bson:"items,omitempty" json:"items,omitempty"`
	// Whole: the whole project (the default); false when Only or Items narrow it.
	Whole bool `bson:"whole" json:"whole"`
	// Kept: the link was removed, but the items cuts here use stay usable.
	Kept      bool      `bson:"kept,omitempty" json:"kept,omitempty"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// LinkFilter is a saved subset of a linked project.
type LinkFilter struct {
	Kinds   []string `bson:"kinds,omitempty" json:"kinds,omitempty" jsonschema:"description=footage\\, resources\\, motion\\, cuts."`
	Tags    []string `bson:"tags,omitempty" json:"tags,omitempty" jsonschema:"description=Tags on footage or resources (MetaRush's suggestions count)."`
	People  []string `bson:"people,omitempty" json:"people,omitempty" jsonschema:"description=Footage where these people speak (person IDs)."`
	Folders []string `bson:"folders,omitempty" json:"folders,omitempty" jsonschema:"description=Resources from these linked folders (folder IDs)."`
}

// Empty reports a filter that narrows nothing.
func (f *LinkFilter) Empty() bool {
	return f == nil || len(f.Kinds)+len(f.Tags)+len(f.People)+len(f.Folders) == 0
}

// LinkItem is one specific thing a link brings in.
type LinkItem struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=footage,enum=resources,enum=motion,enum=cuts"`
	// ID is the asset, resource, motion graphic hash or project ID (a cut: the
	// linked project's cut, any of its versions).
	ID   string `bson:"id" json:"id"`
	Name string `bson:"name,omitempty" json:"name,omitempty"`
}

// BrandKit is a brand — the workspace's, or a project's own (D165): logos,
// colours (taken from the logo automatically, editable) and a font. It feeds
// the cut's brand tokens (spec.brand) and the resource pins templates use
// (spec.resources) — new cuts start with it, and "Use the brand kit" applies
// it to a cut.
type BrandKit struct {
	// The named logos (D198), resource IDs: Logo is the main logo (the one
	// kits had before; templates show it unless one made for their background
	// is there), LogoDark the one for dark backgrounds (usually light or
	// white), LogoLight the one for light backgrounds, LogoIcon a square icon
	// and LogoWordmark the name written out. Cuts pin them as logo, logo-dark,
	// logo-light, logo-icon and logo-wordmark.
	Logo         string `bson:"logo,omitempty" json:"logo,omitempty"`
	LogoDark     string `bson:"logoDark,omitempty" json:"logoDark,omitempty"`
	LogoLight    string `bson:"logoLight,omitempty" json:"logoLight,omitempty"`
	LogoIcon     string `bson:"logoIcon,omitempty" json:"logoIcon,omitempty"`
	LogoWordmark string `bson:"logoWordmark,omitempty" json:"logoWordmark,omitempty"`
	// Colors are the brand tokens (#RRGGBB). Suggested are the dominant colours
	// of the logo; ColorsEdited is set once a person changed them, so a new
	// logo suggests colours without overwriting a person's choice.
	Primary      string   `bson:"primary,omitempty" json:"primary,omitempty"`
	Accent       string   `bson:"accent,omitempty" json:"accent,omitempty"`
	Text         string   `bson:"text,omitempty" json:"text,omitempty"`
	Background   string   `bson:"background,omitempty" json:"background,omitempty"`
	Suggested    []string `bson:"suggested,omitempty" json:"suggested,omitempty"`
	ColorsEdited bool     `bson:"colorsEdited,omitempty" json:"colorsEdited,omitempty"`
	// Font is a bundled font (Inter, Fraunces, JetBrains Mono); FontResource a
	// font file from the library (its licence note is shown with it).
	Font         string    `bson:"font,omitempty" json:"font,omitempty" jsonschema:"enum=Inter,enum=Fraunces,enum=JetBrains Mono"`
	FontResource string    `bson:"fontResource,omitempty" json:"fontResource,omitempty"`
	UpdatedAt    time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy    Actor     `bson:"updatedBy" json:"updatedBy"`
}
