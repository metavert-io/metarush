package conversation

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Imports (D103): the folders call apps leave, recognized by their names.
//
//   - Zoom local recordings: "YYYY-MM-DD HH.MM.SS <topic>/" with video<n>.mp4
//     (the recording), audio<n>.m4a (the call's mix) and, with "Record a
//     separate audio file for each participant", Audio Record/audio<Name><i><n>.m4a
//     per person — since Zoom 5.8 (2021) these start together with the video.
//     Older clients wrote zoom_0.mp4 and audio_only_<i>_<Name>.m4a, which skip
//     the stretches a person was muted (lined up by sound, with a note). Cloud
//     downloads: GMT<date>_Recording….mp4 and "Audio only - <Name>.m4a".
//   - Riverside: riverside_<name>_<raw-video-cfr|raw-audio|…>_<studio>_<n>.<ext>,
//     one person each (raw tracks start when that person started: lined up by sound).
//   - SquadCast and anything else: a folder of two or more recordings, one per
//     person (lined up by sound).
//
// Nothing here talks to a provider's servers; the Zoom cloud API is designed
// in docs/conversations.md, not built.

func init() {
	ops.Def("conversation.detect", "Say whether a set of files (names, as in a folder) is an online call: which app made it, who is in it, which file is whose sound or picture and whether they start together; the Add box offers \"Make a conversation from these\" with it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CallDetectIn) (*CallDetectOut, error) {
			if in.Folder != "" {
				if ops.CallerFrom(ctx).Remote {
					// The dashboard names the files it has; it never reads this computer's folders.
					return nil, invalid("over the dashboard, name the files (files), not a folder")
				}
				files, err := folderFiles(in.Folder)
				if err != nil {
					return nil, err
				}
				in.Files = files
			}
			return detectFiles(in.Files), nil
		})
	ops.Def("conversation.import", "Bring in a call app's recording folder (Zoom with separate audio per participant, Riverside, SquadCast or any folder of per-person recordings) as footage and make a conversation of it, lined up as the app recorded it", ops.Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *CallImportIn) (*CallOut, error) {
			return importFolder(ctx, a, in)
		})
}

// CallDetectFile is one file of a set: its name with its folders ("2026-10-19
// 10.00.00 Garden Show/Audio Record/audioSam2123.m4a"), and the clip it is
// when it was already added.
type CallDetectFile struct {
	Name    string `json:"name" jsonschema:"minLength=1,maxLength=1024"`
	AssetID string `json:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
}

// CallDetectIn names the files (or a folder on this machine).
type CallDetectIn struct {
	Files  []CallDetectFile `json:"files,omitempty" jsonschema:"maxItems=500"`
	Folder string           `json:"folder,omitempty" jsonschema:"description=A folder on this machine (instead of files)."`
}

// CallDetectOut is what the files are.
type CallDetectOut struct {
	// Found: the files make a conversation (at least two recordings).
	Found    bool   `json:"found"`
	Provider string `json:"provider,omitempty" jsonschema:"enum=zoom,enum=riverside,enum=squadcast,enum=folder"`
	Folder   string `json:"folder,omitempty" jsonschema:"description=The recording folder's name."`
	// Aligned: the app's files start together (no lining up by sound needed).
	Aligned bool               `json:"aligned"`
	Name    string             `json:"name,omitempty" jsonschema:"description=A name for the conversation (the meeting's topic)."`
	Members []CallDetectMember `json:"members"`
	Notes   []string           `json:"notes,omitempty"`
	Text    string             `json:"text" jsonschema:"description=What it is, in a sentence."`
}

// CallDetectMember is one file and what it is.
type CallDetectMember struct {
	File    string `json:"file"`
	AssetID string `json:"assetId,omitempty"`
	// Person is who it is ("" for a shared picture); Use what it gives.
	Person string `json:"person,omitempty"`
	Use    string `json:"use,omitempty" jsonschema:"enum=both,enum=sound,enum=picture"`
	// Skip: not part of the conversation (Why says what it is: the call's mix, chat, settings).
	Skip bool   `json:"skip,omitempty"`
	Why  string `json:"why,omitempty"`
}

var (
	videoExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".mkv": true, ".webm": true, ".avi": true}
	audioExt = map[string]bool{".m4a": true, ".wav": true, ".mp3": true, ".aac": true, ".flac": true, ".aif": true, ".aiff": true, ".ogg": true, ".opus": true}

	zoomFolder   = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2}) (\d{2})\.(\d{2})\.(\d{2})(?:\s+(.*?))?(?:\s+(\d{9,11}))?$`)
	zoomVideo    = regexp.MustCompile(`^video(\d{10,12})\.mp4$`)
	zoomMix      = regexp.MustCompile(`^audio(\d{10,12})\.m4a$`)
	zoomPerson   = regexp.MustCompile(`^audio(.+?)(\d{11,14})\.m4a$`)
	zoomLegacyV  = regexp.MustCompile(`^zoom_\d+\.mp4$`)
	zoomLegacyP  = regexp.MustCompile(`^audio_only_\d+_(.+)\.m4a$`)
	zoomCloud    = regexp.MustCompile(`^GMT\d{8}-\d{6}_Recording`)
	zoomCloudP   = regexp.MustCompile(`^Audio only - (.+)\.m4a$`)
	riverside    = regexp.MustCompile(`^riverside_(.+?)_(raw-video-cfr|raw-video|video|raw-audio|compressed-audio|audio)_(.+?)_(\d+)\.(\w+)$`)
	camelBoundry = regexp.MustCompile(`([a-z])([A-Z])`)
)

// detectFiles says what a set of files is.
func detectFiles(files []CallDetectFile) *CallDetectOut {
	out := &CallDetectOut{Members: []CallDetectMember{}}
	type f struct {
		CallDetectFile
		base, dir, ext string
	}
	var media []f
	for _, x := range files {
		n := strings.ReplaceAll(x.Name, "\\", "/")
		base := path.Base(n)
		ext := strings.ToLower(path.Ext(base))
		if strings.HasPrefix(base, ".") || (!videoExt[ext] && !audioExt[ext]) {
			continue
		}
		media = append(media, f{CallDetectFile: x, base: base, dir: path.Dir(n), ext: ext})
	}
	sort.SliceStable(media, func(i, j int) bool { return media[i].Name < media[j].Name })
	member := func(x f, person, use, why string, skip bool) {
		out.Members = append(out.Members, CallDetectMember{File: x.Name, AssetID: x.AssetID, Person: person, Use: use, Skip: skip, Why: why})
	}
	// The recording folder's name (Zoom's has the date and the topic).
	for _, x := range media {
		for _, part := range strings.Split(x.dir, "/") {
			if m := zoomFolder.FindStringSubmatch(part); m != nil {
				out.Folder = part
				if strings.TrimSpace(m[7]) != "" {
					out.Name = strings.TrimSpace(m[7])
				}
			}
		}
	}
	// ---- Zoom ----
	var num string
	for _, x := range media {
		if m := zoomVideo.FindStringSubmatch(x.base); m != nil {
			num = m[1]
		} else if m := zoomMix.FindStringSubmatch(x.base); m != nil && num == "" {
			num = m[1]
		}
	}
	zoomPeople := 0
	for _, x := range media {
		if zoomPersonName(x.base, num) != "" && strings.EqualFold(path.Base(x.dir), "Audio Record") {
			zoomPeople++
		}
	}
	legacy, cloud := 0, 0
	for _, x := range media {
		if zoomLegacyP.MatchString(x.base) {
			legacy++
		}
		if zoomCloudP.MatchString(x.base) {
			cloud++
		}
	}
	switch {
	case zoomPeople > 0:
		out.Provider, out.Aligned = "zoom", true
		for _, x := range media {
			switch {
			case strings.EqualFold(path.Base(x.dir), "Audio Record") && zoomPersonName(x.base, num) != "":
				member(x, zoomPersonName(x.base, num), editspec.UseSound, "", false)
			case zoomVideo.MatchString(x.base):
				member(x, "", editspec.UsePicture, "the meeting's video (its sound is the call's mix)", false)
			case zoomMix.MatchString(x.base):
				member(x, "", "", "the call's mixed sound (each person's own track is used instead)", true)
			default:
				member(x, "", "", "not part of the recording", true)
			}
		}
	case legacy > 0:
		out.Provider = "zoom"
		out.Notes = append(out.Notes, "An older Zoom recording: its tracks leave out the stretches a person was muted, so they are lined up by sound; check the sync.")
		for _, x := range media {
			switch {
			case zoomLegacyP.MatchString(x.base):
				member(x, prettyName(zoomLegacyP.FindStringSubmatch(x.base)[1]), editspec.UseSound, "", false)
			case zoomLegacyV.MatchString(x.base):
				member(x, "", editspec.UsePicture, "the meeting's video (its sound is the call's mix)", false)
			default:
				member(x, "", "", "the call's mixed sound (each person's own track is used instead)", true)
			}
		}
	case cloud > 0:
		out.Provider = "zoom"
		for _, x := range media {
			switch {
			case zoomCloudP.MatchString(x.base):
				member(x, prettyName(zoomCloudP.FindStringSubmatch(x.base)[1]), editspec.UseSound, "", false)
			case zoomCloud.MatchString(x.base) && videoExt[x.ext]:
				if len(out.Members) > 0 && hasPicture(out.Members) {
					member(x, "", "", "another view of the meeting", true)
				} else {
					member(x, "", editspec.UsePicture, "the meeting's video (its sound is the call's mix)", false)
				}
			default:
				member(x, "", "", "the call's mixed sound (each person's own track is used instead)", true)
			}
		}
	}
	if out.Provider == "" {
		// ---- Riverside ----
		type rv struct {
			x    f
			kind string
		}
		people := map[string][]rv{}
		var order []string
		studio := ""
		for _, x := range media {
			if m := riverside.FindStringSubmatch(x.base); m != nil {
				if _, ok := people[m[1]]; !ok {
					order = append(order, m[1])
				}
				people[m[1]] = append(people[m[1]], rv{x, m[2]})
				studio = m[3]
			}
		}
		if len(people) > 0 {
			out.Provider = "riverside"
			if out.Name == "" && studio != "" {
				out.Name = prettyName(strings.ReplaceAll(studio, "_", " "))
			}
			rank := map[string]int{"raw-video-cfr": 0, "raw-video": 1, "video": 2, "raw-audio": 3, "compressed-audio": 4, "audio": 5}
			for _, p := range order {
				fs := people[p]
				sort.SliceStable(fs, func(i, j int) bool { return rank[fs[i].kind] < rank[fs[j].kind] })
				for k, r := range fs {
					use := editspec.UseBoth
					if audioExt[r.x.ext] {
						use = editspec.UseSound
					}
					if k == 0 {
						member(r.x, prettyName(strings.ReplaceAll(p, "_", " ")), use, "", false)
					} else {
						member(r.x, "", "", "another copy of "+prettyName(p)+"'s recording", true)
					}
				}
			}
			for _, x := range media {
				if !riverside.MatchString(x.base) {
					member(x, "", "", "not one of the recording's tracks", true)
				}
			}
		}
	}
	if out.Provider == "" && len(media) >= 2 {
		// ---- any folder: one recording per person ----
		out.Provider = "folder"
		for _, x := range media {
			if strings.Contains(strings.ToLower(x.Name), "squadcast") {
				out.Provider = "squadcast"
			}
		}
		videos := 0
		for _, x := range media {
			if videoExt[x.ext] {
				videos++
			}
		}
		for _, x := range media {
			title := strings.TrimSuffix(x.base, path.Ext(x.base))
			low := strings.ToLower(title)
			switch {
			case videoExt[x.ext] && videos == 1 && len(media) > 2 && sharedView(low):
				member(x, "", editspec.UsePicture, "a shared view of the call", false)
			case videoExt[x.ext]:
				member(x, personFromTitle(title), editspec.UseBoth, "", false)
			default:
				member(x, personFromTitle(title), editspec.UseSound, "", false)
			}
		}
		if out.Name == "" {
			if d := path.Base(media[0].dir); d != "." && d != "/" {
				out.Name = prettyName(d)
			}
		}
	}
	used := 0
	var names []string
	for _, m := range out.Members {
		if !m.Skip {
			used++
			if m.Person != "" {
				names = append(names, m.Person)
			}
		}
	}
	out.Found = used >= 2
	if !out.Found {
		out.Text = "These files aren't a call recording (a conversation needs at least two recordings)."
		return out
	}
	what := map[string]string{"zoom": "A Zoom recording", "riverside": "A Riverside recording", "squadcast": "A SquadCast recording", "folder": "Recordings"}[out.Provider]
	out.Text = fmt.Sprintf("%s with %s", what, joinAnd(names))
	if hasPicture(out.Members) {
		out.Text += ", and the meeting's video"
	}
	if out.Aligned {
		out.Text += "; they start together."
	} else {
		out.Text += "; they will be lined up by their sound."
	}
	if out.Name == "" {
		out.Name = "Conversation with " + joinAnd(names)
	}
	return out
}

func hasPicture(ms []CallDetectMember) bool {
	for _, m := range ms {
		if !m.Skip && m.Use == editspec.UsePicture {
			return true
		}
	}
	return false
}

// zoomPersonName is the person in a Zoom per-participant file name
// ("audioMayaLindqvist11123456789.m4a" → "Maya Lindqvist"): the name, then the
// rejoin index (1–2 digits), then the recording's number.
func zoomPersonName(base, num string) string {
	m := zoomPerson.FindStringSubmatch(base)
	if m == nil {
		return ""
	}
	name, digits := m[1], m[2]
	switch {
	case num != "" && strings.HasSuffix(digits, num) && len(digits)-len(num) <= 2:
	case num != "" && strings.HasSuffix(m[1]+digits, num):
		// The name ends in digits: what is left before the number is the name
		// and a one-digit index.
		rest := strings.TrimSuffix(m[1]+digits, num)
		if len(rest) < 2 {
			return ""
		}
		name = rest[:len(rest)-1]
	case num == "" && len(digits) >= 11:
	default:
		return ""
	}
	return prettyName(name)
}

// prettyName turns a squeezed or slugged name into a person's name:
// "MayaLindqvist" → "Maya Lindqvist", "sam_lee" → "Sam Lee".
func prettyName(s string) string {
	s = camelBoundry.ReplaceAllString(s, "$1 $2")
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	f := strings.Fields(s)
	for i, w := range f {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		f[i] = string(r)
	}
	return strings.Join(f, " ")
}

// personFromTitle is a person's name from a recording's title, without the
// words recordings carry ("Sam cam", "maya_audio_track").
func personFromTitle(title string) string {
	skip := map[string]bool{"cam": true, "camera": true, "webcam": true, "audio": true, "video": true, "mic": true, "track": true, "raw": true, "take": true, "recording": true, "local": true, "iso": true}
	var keep []string
	for _, w := range strings.Fields(prettyName(title)) {
		if !skip[strings.ToLower(w)] && strings.TrimFunc(w, unicode.IsDigit) != "" {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		return prettyName(title)
	}
	return strings.Join(keep, " ")
}

// sharedView reports whether a title sounds like the call's shared view.
func sharedView(t string) bool {
	for _, w := range []string{"gallery", "speaker", "screen", "zoom", "meeting", "call", "program", "mix"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// folderFiles lists a folder's files (two levels down), named with the
// folder's own name first, as a browser sends a dropped folder.
func folderFiles(dir string) ([]CallDetectFile, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, invalid("%s isn't a folder on this computer", filepath.Base(dir))
	}
	root := filepath.Base(filepath.Clean(dir))
	var out []CallDetectFile
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if rel != "." && (strings.Count(rel, string(filepath.Separator)) >= 2 || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(out) < 500 {
			out = append(out, CallDetectFile{Name: root + "/" + filepath.ToSlash(rel)})
		}
		return nil
	})
	return out, err
}

// CallImportIn brings in a recording folder.
type CallImportIn struct {
	ProjectID string `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Folder    string `json:"folder" jsonschema:"minLength=1,description=The recording folder on this machine."`
	Name      string `json:"name,omitempty" jsonschema:"maxLength=120,description=Default: the meeting's topic or the people's names."`
}

func importFolder(ctx context.Context, a *app.App, in *CallImportIn) (*CallOut, error) {
	files, err := folderFiles(in.Folder)
	if err != nil {
		return nil, err
	}
	det := detectFiles(files)
	if !det.Found {
		return nil, invalid("%s", det.Text)
	}
	root := filepath.Clean(in.Folder)
	base := filepath.Base(root)
	var members []CallMemberIn
	for _, m := range det.Members {
		if m.Skip {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(m.File, base+"/")))
		r := ingest.File(ctx, a, p, ingest.Options{ProjectID: in.ProjectID, Via: "cli", Duplicates: "keep", FootageOnly: true})
		id := r.AssetID
		if id == "" && r.Duplicate != nil {
			id = r.Duplicate.AssetID
		}
		if id == "" {
			return nil, invalid("%s couldn't be added: %s", filepath.Base(p), strings.TrimSpace(r.Error))
		}
		members = append(members, CallMemberIn{AssetID: id, Name: m.Person, Use: m.Use})
	}
	name := in.Name
	if name == "" {
		name = det.Name
	}
	align := MethodAudio
	if det.Aligned {
		align = MethodTogether
	}
	out, err := create(ctx, a, &CallCreateIn{ProjectID: in.ProjectID, Name: name, Members: members, Align: align,
		Source: &models.ConversationSource{Provider: det.Provider, Folder: base, Aligned: det.Aligned}})
	if err != nil {
		return nil, err
	}
	out.Summary = det.Text + " " + out.Summary
	for _, n := range det.Notes {
		out.Summary += " " + n
	}
	return out, nil
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("conversation.import", ops.Offering{ID: "call-recordings", Group: "bring", Title: "Call recordings",
		Does:  "Brings in a call app's recording folder, a recording per person, lined up by their sound.",
		Words: []string{"zoom recording", "podcast interview", "remote interview"}, Needs: ops.Needs{Works: []string{"editing"}}})
}
