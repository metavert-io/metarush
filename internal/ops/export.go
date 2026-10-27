package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/version"
)

// ExportTimelineIn writes a revision as a timeline other editing apps open.
type ExportTimelineIn struct {
	RevisionID string `json:"revisionId"`
	Format     string `json:"format" jsonschema:"enum=otio,enum=fcpxml,enum=xmeml,description=otio: OpenTimelineIO (DaVinci Resolve); fcpxml: FCPXML 1.8 (Final Cut Pro\\, Resolve); xmeml: Final Cut Pro 7 XML (Premiere Pro\\, Resolve)."`
	Out        string `json:"out" jsonschema:"description=The timeline file to write on this machine. An existing file is kept unless replace is set."`
	Media      string `json:"media,omitempty" jsonschema:"description=A folder for the original footage under readable names; the timeline points at these files. Copies are instant clones where the disk allows."`
	Link       bool   `json:"link,omitempty" jsonschema:"description=With media: symbolic links to MetaRush's local copies instead of copies (no extra space; they break if those copies are cleaned up)."`
	Replace    bool   `json:"replace,omitempty" jsonschema:"description=Replace the timeline (and its .srt) if they exist. Footage files are never replaced."`
	NoCaptions bool   `json:"noCaptions,omitempty" jsonschema:"description=Do not write the captions as an .srt file next to the timeline."`
}

// ExportMediaFile is one original the timeline points at.
type ExportMediaFile struct {
	AssetID string `json:"assetId"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	// How it got there: copied, cloned (an instant copy), linked, kept (already
	// there from an earlier export) or cache (MetaRush's own copy, no --media).
	How string `json:"how" jsonschema:"enum=copied,enum=cloned,enum=linked,enum=kept,enum=cache"`
}

// ExportTimelineOut is what an export wrote.
type ExportTimelineOut struct {
	Format      string            `json:"format"`
	FormatName  string            `json:"formatName" jsonschema:"description=The format in words (OpenTimelineIO\\, FCPXML 1.8\\, Final Cut Pro 7 XML)."`
	App         string            `json:"app" jsonschema:"description=The editing app this format is for."`
	AlsoOpens   []string          `json:"alsoOpens" jsonschema:"description=Other editing apps that open it."`
	File        string            `json:"file"`
	Captions    string            `json:"captions,omitempty" jsonschema:"description=The .srt file with the captions\\, when the cut has captions."`
	MediaDir    string            `json:"mediaDir,omitempty"`
	Media       []ExportMediaFile `json:"media"`
	Clips       int               `json:"clips"`
	SoundTracks int               `json:"soundTracks"`
	Markers     int               `json:"markers"`
	Duration    float64           `json:"duration"`
	// Notes say, in plain words, what this format could not carry exactly.
	Notes   []string `json:"notes"`
	Summary string   `json:"summary"`
	// Open says how to open the file in the editing app.
	Open string `json:"open"`
}

// FormatApp says what a format is, the app it is for, and how to open it there.
type FormatApp struct {
	Name, App string
	Also      []string
	Open      string
}

// FormatApps describes each timeline format.
var FormatApps = map[string]FormatApp{
	interchange.FormatOTIO:   {"OpenTimelineIO", "DaVinci Resolve", []string{}, "In DaVinci Resolve: File > Import > Timeline…, then choose the .otio file."},
	interchange.FormatFCPXML: {"FCPXML 1.8", "Final Cut Pro", []string{"DaVinci Resolve"}, "In Final Cut Pro: File > Import > XML…, then choose the .fcpxml file."},
	interchange.FormatXMEML:  {"Final Cut Pro 7 XML", "Premiere Pro", []string{"DaVinci Resolve"}, "In Premiere Pro: File > Import…, then choose the .xml file."},
}

func init() {
	Def("export.timeline", "Write a version of a cut as a timeline for DaVinci Resolve (OpenTimelineIO), Final Cut Pro (FCPXML) or Premiere Pro (Final Cut Pro 7 XML), optionally with the original footage under readable names",
		Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *ExportTimelineIn) (*ExportTimelineOut, error) {
			return ExportTimeline(ctx, a, in, nil)
		})
}

// ExportProgress reports footage being fetched or placed.
type ExportProgress func(stage string, done, total int64)

// ExportTimeline is export.timeline (the dashboard's "Send to editing app"
// calls it with a folder MetaRush chooses).
func ExportTimeline(ctx context.Context, a *app.App, in *ExportTimelineIn, progress ExportProgress) (*ExportTimelineOut, error) {
	app, ok := FormatApps[in.Format]
	if !ok {
		return nil, invalid("Choose the timeline format: otio (DaVinci Resolve), fcpxml (Final Cut Pro) or xmeml (Premiere Pro).")
	}
	if strings.TrimSpace(in.Out) == "" {
		return nil, invalid("Say where to save the timeline file.")
	}
	if in.Link && in.Media == "" {
		return nil, invalid("To link the footage instead of copying it, also choose a footage folder.")
	}
	out, err := filepath.Abs(in.Out)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't use that file name.").WithDetails(map[string]any{"out": in.Out})
	}
	if ext := interchange.Extension(in.Format); !strings.EqualFold(filepath.Ext(out), ext) {
		out += ext
	}
	rev, proj, err := getRevision(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	// What may leave (D290): a timeline written where a sync service uploads it.
	for _, dir := range []string{filepath.Dir(out), in.Media} {
		if dir == "" {
			continue
		}
		if err := deliveryMayLeave(ctx, a, dir, deliveryKinds, revisionTouches(ctx, a, rev), "a timeline of “"+proj.Name+"”"); err != nil {
			return nil, err
		}
	}
	srt := strings.TrimSuffix(out, filepath.Ext(out)) + ".srt"
	if _, err := os.Lstat(out); err == nil && !in.Replace {
		return nil, apperr.New(apperr.Conflict, "There's already a file named %s.", filepath.Base(out)).WithDetails(map[string]any{"existing": out}).
			WithHint("Choose another file name, or ask to replace that timeline.")
	}
	// Into a client's delivery folder, a timeline goes out like a video: the
	// ship gate holds it the same way (D298).
	var gate *ShipGateOut
	for _, dir := range []string{filepath.Dir(out), in.Media} {
		if dir == "" || gate != nil {
			continue
		}
		if abs, err := filepath.Abs(dir); err == nil && ClientFolder(a, abs) {
			if gate, err = TimelineGate(ctx, a, proj, rev); err != nil {
				return nil, err
			}
			if !gate.letsOut() {
				return nil, heldError([]*ShipGateOut{gate}, "timeline")
			}
		}
	}
	spec := rev.Spec
	facts, err := loadFactsPinned(ctx, a, rev.ProjectID, &spec, transcripts.PinsOf(rev))
	if err != nil {
		return nil, err
	}
	plan := editspec.Resolve(&spec, facts, rev.ID, rev.ProjectID, rev.SpecHash)
	used := map[string]bool{}
	for _, c := range plan.Clips {
		used[c.AssetID] = true
		if c.Audio != nil && c.Audio.Variant != nil {
			used[c.Audio.Variant.AssetID] = true // the clearer voice it plays (D88)
		}
	}
	for _, x := range plan.Audio {
		used[x.AssetID] = true
		if x.Variant != nil {
			used[x.Variant.AssetID] = true
		}
	}
	var idsUsed []string
	for id := range used {
		idsUsed = append(idsUsed, id)
	}
	sort.Strings(idsUsed)
	res := &ExportTimelineOut{Format: in.Format, FormatName: app.Name, App: app.App, AlsoOpens: app.Also, File: out, Media: []ExportMediaFile{}, Open: app.Open}
	var mediaDir string
	if in.Media != "" {
		if mediaDir, err = filepath.Abs(in.Media); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't use that footage folder.").WithDetails(map[string]any{"media": in.Media})
		}
		if err := os.MkdirAll(mediaDir, 0o755); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the footage folder %s.", filepath.Base(mediaDir)).WithDetails(map[string]any{"folder": mediaDir})
		}
		res.MediaDir = mediaDir
	}
	// Originals come from this machine's cache, else the media store.
	manifest := readMediaManifest(mediaDir)
	taken := map[string]bool{}
	files := map[string]*interchange.Media{}
	for _, id := range idsUsed {
		as := facts.assets[id]
		if as == nil {
			continue
		}
		if c := facts.fixed[id]; c != nil && c.SHA256 != "" {
			// The version plays a fixed copy (D152): the other app gets that file.
			as = fixedAsAsset(as, c)
		}
		if progress != nil {
			progress("Getting "+as.DisplayTitle(), 0, as.Size)
		}
		local, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, func(done, total int64) {
			if progress != nil {
				progress("Getting "+as.DisplayTitle(), done, total)
			}
		})
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "“%s” couldn't be copied to this machine, so the timeline wasn't written.", as.DisplayTitle()).
				WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"assetId": as.ID})
		}
		mf := ExportMediaFile{AssetID: as.ID, Name: filepath.Base(local), Path: local, SHA256: as.SHA256, Size: as.Size, How: "cache"}
		if mediaDir != "" {
			name := mediaName(as, taken, manifest, mediaDir)
			mf.Name, mf.Path = name, filepath.Join(mediaDir, name)
			how, err := placeMedia(local, mf.Path, as, in.Link, manifest)
			if err != nil {
				return nil, err
			}
			mf.How = how
			manifest[name] = mediaManifestEntry{SHA256: as.SHA256, Size: as.Size, Linked: in.Link}
		}
		res.Media = append(res.Media, mf)
		files[id] = exportMedia(as, mf)
	}
	if mediaDir != "" {
		writeMediaManifest(mediaDir, manifest)
	}
	names := map[string]string{}
	for _, c := range spec.Clips {
		if c.Label != "" {
			names[c.ID] = c.Label
		}
	}
	title := proj.Name
	if spec.Title != "" && spec.Title != proj.Name {
		title += ", " + spec.Title
	}
	tl, err := interchange.Build(plan, interchange.BuildOptions{Name: fmt.Sprintf("%s (version %d)", title, rev.Number), Spec: &spec, Names: names,
		Media: func(id string) *interchange.Media { return files[id] }})
	if err != nil {
		return nil, apperr.Wrap(apperr.Validation, err, "This version can't be exported as a timeline: %v.", err)
	}
	meta := map[string]any{"project": proj.Name, "projectId": proj.ID, "revisionId": rev.ID, "revision": rev.Number, "specHash": rev.SpecHash,
		"generator": "metarush " + version.Version}
	var body []byte
	var notes []string
	switch in.Format {
	case interchange.FormatOTIO:
		body, notes, err = interchange.WriteOTIO(tl, meta)
	case interchange.FormatFCPXML:
		body, notes, err = interchange.WriteFCPXML(tl, meta)
	case interchange.FormatXMEML:
		body, notes, err = interchange.WriteXMEML(tl, meta)
	}
	if err != nil {
		return nil, err
	}
	if mediaDir == "" {
		notes = append(notes, "the timeline uses MetaRush's own copies of the footage on this machine; export with a footage folder to get copies with readable names that you can move")
	}
	// The captions file is shared by every format written next to it: the
	// same captions are kept, different ones need replace.
	var subs []byte
	if !in.NoCaptions {
		subs = interchange.WriteSRT(tl)
	}
	writeSubs := subs != nil
	if subs != nil {
		if old, err := os.ReadFile(srt); err == nil {
			if string(old) == string(subs) {
				writeSubs = false
			} else if !in.Replace {
				return nil, apperr.New(apperr.Conflict, "There's already a file named %s, with other captions.", filepath.Base(srt)).WithDetails(map[string]any{"existing": srt}).
					WithHint("Choose another file name, or ask to replace the timeline and its captions.")
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the folder %s.", filepath.Base(filepath.Dir(out))).WithDetails(map[string]any{"folder": filepath.Dir(out)})
	}
	if err := writeFileAtomic(out, body, in.Replace); err != nil {
		return nil, err
	}
	if subs != nil {
		if writeSubs {
			if err := writeFileAtomic(srt, subs, in.Replace); err != nil {
				return nil, err
			}
		}
		res.Captions = srt
	}
	for _, t := range tl.Video {
		res.Clips += len(t.Items)
	}
	for _, t := range tl.Audio {
		if len(t.Items) > 0 {
			res.SoundTracks++
		}
	}
	res.Markers = len(tl.Markers)
	res.Duration = float64(tl.Duration) / float64(tl.FPS)
	res.Notes = notes
	if res.Notes == nil {
		res.Notes = []string{}
	}
	res.Summary = fmt.Sprintf("Wrote version %d of %s for %s: %s, %s and %s.", rev.Number, proj.Name, app.App, plural(res.Clips, "clip"), plural(res.SoundTracks, "sound track"), plural(res.Markers, "marker"))
	if len(app.Also) > 0 {
		res.Summary += " " + strings.Join(app.Also, " and ") + " opens it too."
	}
	a.Audit(ctx, "export.timeline", "revision", rev.ID, proj.ID, map[string]any{"format": in.Format, "file": out, "media": mediaDir, "link": in.Link, "files": len(res.Media)})
	if gate != nil && gate.On {
		if notes := gate.record().SentAnyway; len(notes) > 0 {
			res.Notes = append(res.Notes, "It goes into a client's delivery folder past what held the version, as a person decided; the receipt says so.")
			if err := recordSentOut(ctx, a, rev.ID, notes, "timeline", filepath.Base(filepath.Dir(out))); err != nil {
				a.Audit(ctx, "delivery.sentOut.failed", "revision", rev.ID, rev.ProjectID, map[string]any{"error": err.Error(), "what": "timeline"})
				res.Notes = append(res.Notes, fmt.Sprintf(sentOutNote, rev.Number))
			}
		}
	}
	return res, nil
}

// fixedAsAsset is a clip as its fixed copy: the copy's file, size and
// facts under the clip's ID, named "<title> (fixed)" (D152).
func fixedAsAsset(as *models.Asset, c *models.SourceCopy) *models.Asset {
	cp := *as
	cp.StorageKey, cp.SHA256, cp.Size = c.StorageKey, c.SHA256, c.Size
	if c.Media != nil {
		cp.Media = *c.Media
	}
	ext := filepath.Ext(as.Name)
	switch c.MIME {
	case "video/mp4":
		ext = ".mp4"
	case "video/quicktime":
		ext = ".mov"
	case "audio/flac":
		ext = ".flac"
	}
	if c.MIME != "" {
		cp.MIME = c.MIME
	}
	cp.Title = as.DisplayTitle() + " (fixed)"
	cp.Name = strings.TrimSuffix(as.Name, filepath.Ext(as.Name)) + " (fixed)" + ext
	return &cp
}

func exportMedia(as *models.Asset, mf ExportMediaFile) *interchange.Media {
	m := &interchange.Media{ID: as.ID, Name: mf.Name, Path: mf.Path, SHA256: as.SHA256, Duration: as.Media.Duration, FPS: as.Media.FPS,
		Width: as.Media.Width, Height: as.Media.Height, HasVideo: as.Media.HasVideo, HasAudio: as.Media.HasAudio}
	for _, s := range as.Media.Streams {
		if s.Type == "audio" {
			m.Channels, m.SampleRate = s.Channels, s.SampleRate
			break
		}
	}
	if m.Name == "" || strings.HasPrefix(m.Name, as.SHA256) {
		m.Name = readableName(as)
	}
	return m
}

// readableName is a file name people recognize: the clip's title and its
// original extension, without characters file systems refuse.
func readableName(as *models.Asset) string {
	ext := filepath.Ext(as.Name)
	if ext == "" {
		switch {
		case strings.HasPrefix(as.MIME, "video/"):
			ext = ".mp4"
		case strings.HasPrefix(as.MIME, "audio/"):
			ext = ".m4a"
		}
	}
	base := strings.TrimSpace(as.DisplayTitle())
	if base == "" {
		base = strings.TrimSuffix(as.Name, filepath.Ext(as.Name))
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			return '-'
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, base)
	base = strings.Trim(base, ". ")
	if len(base) > 120 {
		base = strings.TrimSpace(base[:120])
	}
	if base == "" {
		base = "clip"
	}
	return base + ext
}

// mediaName picks a free readable name in the media folder: the same file
// from an earlier export keeps its name; another file gets " 2", " 3"…
func mediaName(as *models.Asset, taken map[string]bool, manifest map[string]mediaManifestEntry, dir string) string {
	want := readableName(as)
	ext := filepath.Ext(want)
	stem := strings.TrimSuffix(want, ext)
	for n := 1; ; n++ {
		name := want
		if n > 1 {
			name = fmt.Sprintf("%s %d%s", stem, n, ext)
		}
		key := strings.ToLower(name)
		if taken[key] {
			continue
		}
		if e, ok := manifest[name]; ok && e.SHA256 == as.SHA256 {
			taken[key] = true
			return name
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			continue // someone else's file (or another clip's): never replaced
		}
		taken[key] = true
		return name
	}
}

// placeMedia puts a verified local original at dst: kept when an earlier
// export already put the same bytes there, a symbolic link with link, else an
// instant copy-on-write clone where the disk supports it, else a copy that is
// hashed as it is written.
func placeMedia(src, dst string, as *models.Asset, link bool, manifest map[string]mediaManifestEntry) (string, error) {
	if st, err := os.Lstat(dst); err == nil {
		e, ok := manifest[filepath.Base(dst)]
		if ok && e.SHA256 == as.SHA256 {
			if (st.Mode()&os.ModeSymlink != 0) == link || st.Mode().IsRegular() {
				return "kept", nil
			}
		}
		return "", apperr.New(apperr.Conflict, "The footage folder already has a file named %s, and MetaRush never replaces files there.", filepath.Base(dst)).
			WithHint("Choose another footage folder, or move that file first.").WithDetails(map[string]any{"path": dst})
	}
	if link {
		if err := os.Symlink(src, dst); err != nil {
			return "", apperr.Wrap(apperr.Storage, err, "MetaRush couldn't link %s in the footage folder.", filepath.Base(dst)).WithDetails(map[string]any{"path": dst})
		}
		return "linked", nil
	}
	tmp := dst + ".part"
	_ = os.Remove(tmp)
	if err := cloneFile(src, tmp); err == nil {
		if st, err := os.Stat(tmp); err == nil && st.Size() == as.Size {
			_ = os.Chmod(tmp, 0o644)
			if err := placeNoClobber(tmp, dst); err != nil {
				return "", err
			}
			return "cloned", nil
		}
		_ = os.Remove(tmp)
	}
	if err := copyVerified(src, dst, as.SHA256, "the original footage"); err != nil {
		return "", err
	}
	return "copied", nil
}

// The media folder remembers what earlier exports put in it, so exporting
// again keeps those files instead of hashing them again.
type mediaManifestEntry struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Linked bool   `json:"linked,omitempty"`
}

const mediaManifestName = ".metarush-media.json"

func readMediaManifest(dir string) map[string]mediaManifestEntry {
	out := map[string]mediaManifestEntry{}
	if dir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(dir, mediaManifestName))
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	// Trust an entry only while the file still has that size (or is a link).
	for name, e := range out {
		st, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || (st.Mode().IsRegular() && st.Size() != e.Size) {
			delete(out, name)
		}
	}
	return out
}

func writeMediaManifest(dir string, m map[string]mediaManifestEntry) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(filepath.Join(dir, mediaManifestName), append(b, '\n'), true)
}

// writeFileAtomic writes a file through a temporary name; without replace an
// existing file is never overwritten.
func writeFileAtomic(path string, b []byte, replace bool) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't save %s there.", filepath.Base(path)).WithDetails(map[string]any{"path": path})
	}
	if replace {
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	return placeNoClobber(tmp, path)
}

// ExportsFolder is where the dashboard puts timelines it sends to an editing
// app: ~/Movies/MetaRush exports on macOS, ~/Videos/MetaRush exports elsewhere
// (METARUSH_EXPORTS_DIR overrides it).
func ExportsFolder() (string, error) {
	if d := os.Getenv("METARUSH_EXPORTS_DIR"); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Movies", "MetaRush exports"), nil
	}
	return filepath.Join(home, "Videos", "MetaRush exports"), nil
}

var errNoClone = errors.New("clone not supported")

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("export.timeline", Offering{ID: "timelines", Group: "deliver", Title: "Timelines for other editors",
		Does:  "Writes a version of a cut as a timeline another editing app opens, with its clips in place.",
		Words: []string{"xml", "edl", "finish in another editor"}})
}
