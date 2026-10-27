package aiwork

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// Lip sync changes every face in the part it is sent, and the on-screen
// index only looked at samples of the clip (a second or more apart): a face
// on screen between two samples was never seen, so never named. Before
// anything is sent, MetaRush looks at the part it sends every quarter
// second and refuses a moment showing more faces than people named there
// (D279).

// lipEvery is how often the part is looked at, in seconds.
const lipEvery = 0.25

// lipFaceMin is how sure Apple Vision must be to count a face, as the
// on-screen index counts one.
const lipFaceMin = 0.5

// lipMaxFrames bounds the look (ten minutes of footage).
const lipMaxFrames = 2400

// lookAtFrames reads the JPEG frames in a folder with the on-screen helper,
// in file order (tests put a stand-in in its place).
var lookAtFrames = func(ctx context.Context, a *app.App, dir string) ([]vision.Frame, error) {
	var out []vision.Frame
	err := vision.Analyze(ctx, a.Tools, dir, vision.Options{Text: "off"}, func(f vision.Frame) { out = append(out, f) })
	return out, err
}

// lipFacesNamed looks at the footage a lip sync sends, every quarter
// second, and refuses a moment with a face nobody named there.
func lipFacesNamed(ctx context.Context, a *app.App, g *models.Generation) error {
	if g.Kind != aiservices.KindLipSync {
		return nil
	}
	var ref *models.GenReference
	for i := range g.References {
		if g.References[i].Role == aiservices.RoleMedia && g.References[i].AssetID != "" {
			ref = &g.References[i]
			break
		}
	}
	if ref == nil {
		return apperr.New(apperr.Validation, "MetaRush can't tell which footage this lip sync would change.")
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.AssetID, &as); err != nil {
		return apperr.New(apperr.Validation, "The footage this lip sync would change isn't in MetaRush any more.")
	}
	cantLook := func(why string) error {
		return apperr.New(apperr.NotConfigured, "Before lip sync MetaRush looks at every quarter second of the footage for faces, and %s.", why).
			WithHint("Ask again on a Mac that can look at what's on screen.")
	}
	if !a.Tools.Has(tools.FFmpeg) {
		return cantLook("this computer can't take frames from video")
	}
	path, err := generate.Local(ctx, a, ref.StorageKey, ref.SHA256, ref.Size)
	if err != nil {
		return apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the footage for this request from its storage.")
	}
	first, done, err := generate.Scratch(a, "f-00001.jpg")
	if err != nil {
		return err
	}
	defer done()
	dir := filepath.Dir(first)
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=%g,scale='min(640,iw)':-2", 1/lipEvery), "-frames:v", strconv.Itoa(lipMaxFrames), "-q:v", "4", filepath.Join(dir, "f-%05d.jpg")}}); err != nil {
		return cantLook("MetaRush couldn't take frames from it")
	}
	frames, err := lookAtFrames(ctx, a, dir)
	if err != nil || len(frames) == 0 {
		return cantLook("this computer can't look at what's on screen")
	}
	// Who the index found, and where: a track's ends are only known to a
	// sample, so a named face counts up to one sample's step beyond them.
	var tracks []models.FaceTrack
	_ = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": as.ID, "personId": bson.M{"$exists": true, "$ne": ""}}, &tracks)
	step := visual.StepFor(as.Media.Duration)
	sort.Slice(frames, func(i, j int) bool { return frames[i].File < frames[j].File })
	for _, f := range frames {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f.File, "f-"), ".jpg"))
		if err != nil || n < 1 {
			continue
		}
		at := ref.From + float64(n-1)*lipEvery
		seen := 0
		for _, fc := range f.Faces {
			if fc.Conf >= lipFaceMin {
				seen++
			}
		}
		named, byApp := map[string]bool{}, ""
		for _, t := range tracks {
			if t.From-step <= at && at <= t.To+step {
				if t.NamedByPerson() { // only a name a person gave counts (D274)
					named[t.PersonID] = true
				} else if byApp == "" {
					byApp = fmt.Sprintf(" (%s named one as %s; only a person's names count)", t.Naming.Words(), t.Name)
				}
			}
		}
		if seen > len(named) {
			return apperr.New(apperr.ConsentRequired, "At %s “%s” shows %s, and %s named there%s. Lip sync changes every face it shows, so name everyone seen in that part, with their consent to lip sync, or pick another part.",
				clock(at), as.DisplayTitle(), countWords(seen, "face"), namedWords(len(named)), byApp)
		}
	}
	return nil
}

func countWords(n int, what string) string {
	if n == 1 {
		return "a " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func namedWords(n int) string {
	switch n {
	case 0:
		return "nobody is"
	case 1:
		return "only one person is"
	default:
		return fmt.Sprintf("only %d people are", n)
	}
}
