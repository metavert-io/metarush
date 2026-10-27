package sources

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// Descriptive names (D150): what MetaRush calls a source, from its first look
// at it. The facts are gathered once (look), then named (name), so the rules
// are pure and testable.

// Look is what MetaRush knows about a source for its name.
type Look struct {
	What string // call | screen | video | voice | music
	// People are the named voices and faces, the most present first.
	People []string
	// Topic is what is said most ("the garden"); Screen a title shown on
	// screen near the start; Link a linked post's title; TagTitle and
	// TagArtist the file's own tags.
	Topic, Screen, Link string
	TagTitle, TagArtist string
	// Recorded is when it was recorded (zero: unknown); Duration its length.
	Recorded time.Time
	Duration float64
	// FileName is its original file name; CameraFile says the name is one a
	// camera or phone made up (IMG_0042), so a date says more.
	FileName   string
	CameraFile bool
	// Keep: a generated shot or a Remotion composition keeps the name it came with.
	Keep bool
}

var cameraFileRe = regexp.MustCompile(`(?i)^(img|dsc|dscn|dscf|dji|gx|gopr|gp|mvi|mov|pxl|vid|video|clip|movie|rec|recording|trim|mah|dcim|p\d|c\d|_mg|sam|wp|hd)[ _-]?\d|^\d[\d _.-]*$|^[0-9a-f]{8}-[0-9a-f]{4}-|^(new recording|recording|voice memo|audio)( \d+)?$|^movie on \d`)

// cameraName says whether a file name is one a camera or phone made up.
func cameraName(name string) bool {
	base := strings.TrimSpace(models.DefaultTitle(name))
	return cameraFileRe.MatchString(base)
}

// look gathers the facts for a source's name.
func look(ctx context.Context, a *app.App, as *models.Asset) Look {
	_, what := Kind(ctx, a, as)
	l := Look{What: what, Duration: as.Media.Duration, FileName: as.Name, CameraFile: cameraName(as.Name),
		TagTitle: as.Media.TagTitle, TagArtist: as.Media.TagArtist, Recorded: recordedAt(as)}
	if src := as.Source; src != nil {
		switch {
		case src.IsGenerated() || src.Platform == models.PlatformRemotion:
			l.Keep = true
			return l
		default:
			l.Link = strings.TrimSpace(src.Title)
			// One post with several videos: the import told them apart in their titles ("… (video 1 of 2)").
			if t := strings.TrimSpace(as.Title); l.Link != "" && len(t) > len(l.Link) && strings.HasPrefix(t, l.Link) {
				l.Link = t
			}
		}
	}
	l.People = peopleIn(ctx, a, as.ID)
	if as.Transcript != nil && as.Transcript.State == "available" {
		l.Topic = Topic(transcriptTexts(ctx, a, as.ID))
	}
	if as.Media.HasVideo {
		l.Screen = screenTitle(ctx, a, as)
	}
	return l
}

func recordedAt(as *models.Asset) time.Time {
	for _, f := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, as.Media.CreationTime); err == nil && t.Year() > 1990 {
			return t
		}
	}
	return time.Time{}
}

// peopleIn lists the named voices (by how long each speaks) and then the
// named faces (by how often each is seen), at most three, each once.
func peopleIn(ctx context.Context, a *app.App, assetID string) []string {
	var out []string
	seen := map[string]bool{}
	addName := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] || len(out) >= 3 {
			return
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	var voices []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": assetID, "name": bson.M{"$nin": bson.A{nil, ""}}, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &voices,
		options.Find().SetSort(bson.D{{Key: "seconds", Value: -1}}))
	for _, v := range voices {
		if !v.Minor {
			addName(v.Name)
		}
	}
	var faces []models.FaceTrack
	_ = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": assetID, "name": bson.M{"$nin": bson.A{nil, ""}}}, &faces,
		options.Find().SetSort(bson.D{{Key: "samples", Value: -1}}))
	for _, f := range faces {
		addName(f.Name)
	}
	return out
}

func transcriptTexts(ctx context.Context, a *app.App, assetID string) []string {
	var segs []struct {
		Text string `bson:"text"`
	}
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs,
		options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}).SetProjection(bson.M{"text": 1}).SetLimit(4000))
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Text)
	}
	return out
}

// screenTitle is a title shown on screen near the start: two to eight words
// that stay put (not a clock) in the first tenth of the clip (at most 15 s),
// the biggest such text.
func screenTitle(ctx context.Context, a *app.App, as *models.Asset) string {
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID)
	if st == nil || st.Index == nil {
		return ""
	}
	limit := math.Min(15, math.Max(4, as.Media.Duration*0.1))
	best, bestArea := "", 0.0
	for _, t := range st.Index.Text {
		if t.Changing || t.From > limit || t.Conf < 0.5 {
			continue
		}
		words := strings.Fields(t.Text)
		if len(words) < 2 || len(words) > 8 {
			continue
		}
		area := t.Box[2] * t.Box[3] * math.Min(3, t.To-t.From)
		if area > bestArea {
			best, bestArea = strings.Join(words, " "), area
		}
	}
	return best
}

// name turns a look into a title and the plain reason for it ("" when the
// file name should stay).
func name(l Look) (title, why string, basis []string) {
	if l.Keep {
		return "", "", nil
	}
	dur := lengthWords(l.Duration)
	withDur := func(s string) string {
		if dur == "" {
			return s
		}
		return s + " (" + dur + ")"
	}
	people := l.People
	switch {
	case l.Link != "":
		return capFirst(l.Link), "the title of the post it was imported from", []string{"link"}
	case l.What == whatMusic:
		if l.TagTitle != "" {
			if l.TagArtist != "" {
				return capFirst(l.TagTitle) + " by " + l.TagArtist, "the title and artist the file carries", []string{"tags"}
			}
			return capFirst(l.TagTitle), "the title the file carries", []string{"tags"}
		}
		return "", "", nil
	case l.What == whatCall:
		switch len(people) {
		case 0:
			if d := dayWords(l.Recorded); d != "" {
				return withDur("Call from " + d), "the day the call was recorded", []string{"kind", "date"}
			}
			return "", "", nil
		case 1:
			return withDur("Call with " + people[0]), "who is heard or seen in this call", []string{"kind", "people"}
		default:
			return withDur(possessive(people[0]) + " call with " + joinAnd(people[1:])), "who is heard or seen in this call", []string{"kind", "people"}
		}
	case l.What == whatScreen:
		switch {
		case len(people) > 0:
			return withDur(possessive(people[0]) + " screen recording"), "who talks in this screen recording", []string{"kind", "people"}
		case l.Screen != "":
			return withDur("Screen recording: " + titleCase(l.Screen)), "the words on screen at the start", []string{"kind", "screen-text"}
		case l.Topic != "":
			return withDur("Screen recording about " + l.Topic), "what is said most in it", []string{"kind", "topic"}
		}
	case len(people) > 0:
		verb := "talks"
		subject := people[0]
		if len(people) > 1 {
			verb, subject = "talk", joinAnd(people)
		}
		if l.Topic != "" {
			return withDur(subject + " " + verb + " about " + l.Topic), "who is in it and what is said most", []string{"people", "topic"}
		}
		if l.What == whatVoice && len(people) == 1 {
			return withDur(possessive(people[0]) + " voice recording"), "whose voice it is", []string{"people"}
		}
		return withDur(subject + " " + strings.TrimSuffix(verb, "s") + "ing"), "who is heard or seen in it", []string{"people"}
	case l.Screen != "":
		return withDur(titleCase(l.Screen)), "the title on screen at the start", []string{"screen-text"}
	case l.Topic != "":
		if l.What == whatVoice {
			return withDur("Voice recording about " + l.Topic), "what is said most in it", []string{"topic"}
		}
		return withDur("Video about " + l.Topic), "what is said most in it", []string{"topic"}
	case l.TagTitle != "" && !cameraName(l.TagTitle):
		return capFirst(l.TagTitle), "the title the file carries", []string{"tags"}
	}
	if l.CameraFile {
		if d := dayTimeWords(l.Recorded); d != "" {
			kind := "Video"
			if l.What == whatVoice {
				kind = "Recording"
			}
			return withDur(kind + " from " + d), "when it was recorded", []string{"date"}
		}
	}
	return "", "", nil
}

// lengthWords: "32 min", "45 s", "1 h 05 min".
func lengthWords(sec float64) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 90:
		return fmt.Sprintf("%d s", int(math.Round(sec)))
	case sec < 3600:
		return fmt.Sprintf("%d min", int(math.Round(sec/60)))
	}
	h := int(sec / 3600)
	m := int(math.Round((sec - float64(h)*3600) / 60))
	return fmt.Sprintf("%d h %02d min", h, m)
}

func dayWords(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2 Jan 2006")
}

func dayTimeWords(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	l := t.Local()
	return l.Format("2 Jan 2006") + ", " + strings.ToLower(l.Format("3:04 PM"))
}

func possessive(n string) string {
	if strings.HasSuffix(n, "s") {
		return n + "'"
	}
	return n + "'s"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func capFirst(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}

// titleCase turns SHOUTED on-screen text into Title Case; other text keeps its
// own capitals.
func titleCase(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if strings.ToUpper(s) != s || strings.ToLower(s) == s {
		return capFirst(s)
	}
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 && i < len(words)-1 && len(w) <= 3 && (w == "of" || w == "and" || w == "the" || w == "a" || w == "an" || w == "to" || w == "in" || w == "on" || w == "for" || w == "at") {
			continue
		}
		words[i] = capFirst(w)
	}
	return strings.Join(words, " ")
}

// Suggest is MetaRush's descriptive name for a source (nil when the file
// name says as much as MetaRush knows, or the source keeps its own name).
func Suggest(ctx context.Context, a *app.App, as *models.Asset) *models.NameSuggestion {
	title, why, basis := name(look(ctx, a, as))
	if title == "" {
		return nil
	}
	if r := []rune(title); len(r) > 200 {
		title = strings.TrimSpace(string(r[:199])) + "…"
	}
	sort.Strings(basis)
	return &models.NameSuggestion{Title: title, Why: "From " + why + ".", Basis: basis, At: time.Now().UTC()}
}
