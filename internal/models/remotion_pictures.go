package models

import "time"

// Pictures as settings (D209), the words-as-settings pattern (D168, D169)
// applied to a Remotion project's pictures: one change a person approves
// wraps each picture its code draws in picture("NAME", …); after that a
// person (or Claude) replaces a picture as data — a checked, content-addressed
// resource served to the jailed render by its SHA-256 — with no new version
// of the code and no new approval.

// How the code names a picture.
const (
	RemotionPictureStatic = "static" // staticFile("hero.png"): a file in public/
	RemotionPictureImport = "import" // import hero from "./hero.png"
	RemotionPictureCode   = "code"   // worked out as the video plays (stays code)
)

// RemotionPicture is a picture a version's code draws: a file of the project
// (public/hero.png, src/art/logo.svg), its name as a setting (HERO), and where
// the code names it.
type RemotionPicture struct {
	Name string `bson:"name" json:"name" jsonschema:"description=Its name as a setting (HERO); the name it would get before it is one."`
	File string `bson:"file,omitempty" json:"file,omitempty" jsonschema:"description=The project's own file (public/hero.png); empty when the code names it in a way MetaRush can't follow."`
	// Setting: the code reads it through picture(NAME, …) (a person approved that change).
	Setting bool                 `bson:"setting,omitempty" json:"setting,omitempty"`
	Uses    []RemotionPictureUse `bson:"uses" json:"uses"`
}

// RemotionPictureUse is one place the code names a picture.
type RemotionPictureUse struct {
	File string `bson:"file" json:"file"`
	Line int    `bson:"line" json:"line"`
	How  string `bson:"how" json:"how" jsonschema:"enum=static,enum=import,enum=code"`
	// Wrapped: it is read through picture(NAME, …) already.
	Wrapped bool `bson:"wrapped,omitempty" json:"wrapped,omitempty"`
	// Code is the reference as written (references that stay code).
	Code string `bson:"code,omitempty" json:"code,omitempty"`
	// Why it can't become a setting (empty when it can).
	Why string `bson:"why,omitempty" json:"why,omitempty"`
}

// RemotionPictureValue is a picture that replaces one of the project's own:
// a version of a resource (checked: a raster picture or a cleaned SVG, not too
// large), served to renders by its SHA-256.
type RemotionPictureValue struct {
	Name       string `bson:"name" json:"name"`
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	Resource   string `bson:"resource,omitempty" json:"resource,omitempty" jsonschema:"description=The resource's short name."`
	Title      string `bson:"title,omitempty" json:"title,omitempty"`
	// SHA256 of the version used (empty in a change: back to the code's own picture).
	SHA256 string `bson:"sha256,omitempty" json:"sha256,omitempty"`
	Ext    string `bson:"ext,omitempty" json:"ext,omitempty" jsonschema:"enum=,enum=png,enum=jpg,enum=webp,enum=gif,enum=svg"`
	Size   int64  `bson:"size,omitempty" json:"size,omitempty"`
	Width  int    `bson:"width,omitempty" json:"width,omitempty"`
	Height int    `bson:"height,omitempty" json:"height,omitempty"`
}

// Served is the file name a render serves it under ("<sha256>.<ext>").
func (v RemotionPictureValue) Served() string { return v.SHA256 + "." + v.Ext }

// RemotionPictures are the pictures people put in place of the project's own (D209).
type RemotionPictures struct {
	// Number counts the changes (1, 2, 3 …); every change makes new renders.
	Number int                    `bson:"number" json:"number"`
	Values []RemotionPictureValue `bson:"values" json:"values" jsonschema:"description=The pictures that replace the project's own."`
	At     time.Time              `bson:"at" json:"at"`
	By     Actor                  `bson:"by" json:"by"`
	// Changes are the latest changes, newest first (at most 50).
	Changes []RemotionPicturesChange `bson:"changes,omitempty" json:"changes,omitempty"`
}

// RemotionPicturesChange is one change of pictures and what checking it found
// (the same check as a change of words: lengths, problems, before/after stills).
type RemotionPicturesChange struct {
	Number  int                    `bson:"number" json:"number"`
	At      time.Time              `bson:"at" json:"at"`
	By      Actor                  `bson:"by" json:"by"`
	Set     []RemotionPictureValue `bson:"set" json:"set" jsonschema:"description=The pictures it changed (no sha256: back to the code's own picture)."`
	Note    string                 `bson:"note,omitempty" json:"note,omitempty"`
	Summary string                 `bson:"summary,omitempty" json:"summary,omitempty"`
	Check   *RemotionWordsCheck    `bson:"check,omitempty" json:"check,omitempty"`
}

// Picture finds a version's picture by its name.
func (v *RemotionVersion) Picture(name string) *RemotionPicture {
	for i := range v.Pictures {
		if v.Pictures[i].Name == name {
			return &v.Pictures[i]
		}
	}
	return nil
}

// HasPictureSettings reports a version whose code reads pictures from settings.
func (v *RemotionVersion) HasPictureSettings() bool {
	for _, p := range v.Pictures {
		if p.Setting {
			return true
		}
	}
	return false
}
