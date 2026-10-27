package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/version"
)

// DeliverIn exports finished renders of revisions to a local folder.
type DeliverIn struct {
	RevisionIDs []string `json:"revisionIds" jsonschema:"minItems=1,maxItems=50"`
	Out         string   `json:"out" jsonschema:"description=Destination folder on this machine (created if missing). Existing files are never overwritten."`
	Presets     []string `json:"presets,omitempty" jsonschema:"description=Only these output presets (default: every preset rendered for the revision)."`
	Quality     string   `json:"quality,omitempty" jsonschema:"enum=final,enum=review,description=Which renders to deliver (default final)."`
	AllowDraft  bool     `json:"allowDraft,omitempty" jsonschema:"description=Also export revisions nobody has approved yet. Only when the user explicitly asks."`
}

// DeliverFile is one exported video and its sidecar.
type DeliverFile struct {
	RevisionID     string  `json:"revisionId"`
	RevisionNumber int     `json:"revisionNumber"`
	Title          string  `json:"title"`
	Approval       string  `json:"approval"`
	RenderID       string  `json:"renderId"`
	Preset         string  `json:"preset"`
	Quality        string  `json:"quality"`
	Duration       float64 `json:"duration"`
	Path           string  `json:"path"`
	Sidecar        string  `json:"sidecar"`
	// Captions are the captions files written next to it (SubRip, WebVTT).
	Captions  []string `json:"captions,omitempty"`
	Size      int64    `json:"size"`
	SHA256    string   `json:"sha256"`
	QAVerdict string   `json:"qaVerdict,omitempty"`
	// SentAnyway are the notes that sent it out past what held it (D299), each with its person.
	SentAnyway []models.ShipOverride `json:"sentAnyway,omitempty"`
}

// DeliverOut lists what was written.
type DeliverOut struct {
	Dir      string        `json:"dir"`
	Files    []DeliverFile `json:"files"`
	Warnings []string      `json:"warnings"`
	Note     string        `json:"note"`
	// Credits list the outside footage the videos use, for information
	// (descriptions, end credits); they never block a delivery.
	Credits []string `json:"credits,omitempty"`
	// Disclosure is set when a video shows AI-generated footage (D127): what
	// it shows and what each place asks people to disclose when they post it.
	Disclosure *DeliveryGenerated `json:"disclosure,omitempty"`
	// Signoffs are the OKs still waiting for the delivered cuts (D166): a
	// reminder (also in Warnings), never a lock.
	Signoffs []models.Signoff `json:"signoffs,omitempty"`
}

// DeliverySidecar is the JSON written next to every delivered file.
type DeliverySidecar struct {
	Format  string `json:"format"` // metarush.delivery/1
	File    string `json:"file"`
	Title   string `json:"title"`
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	Revision struct {
		ID          string          `json:"id"`
		Number      int             `json:"number"`
		Branch      string          `json:"branch"`
		SpecHash    string          `json:"specHash"`
		Approval    models.Approval `json:"approval"`
		Instruction string          `json:"instruction"`
		Rationale   string          `json:"rationale"`
		Notes       string          `json:"notes,omitempty"`
	} `json:"revision"`
	Render struct {
		ID               string            `json:"id"`
		Preset           string            `json:"preset"`
		Quality          string            `json:"quality"`
		Width            int               `json:"width"`
		Height           int               `json:"height"`
		FPS              float64           `json:"fps"`
		Duration         float64           `json:"duration"`
		SHA256           string            `json:"sha256"`
		Size             int64             `json:"size"`
		RenderedAt       *time.Time        `json:"renderedAt,omitempty"`
		RendererVersion  string            `json:"rendererVersion,omitempty"`
		TemplateVersions map[string]string `json:"templateVersions,omitempty"`
	} `json:"render"`
	Sources     []DeliverySource `json:"sources"`
	MusicRights []DeliveryRights `json:"musicRights"`
	Claims      []DeliveryClaim  `json:"claims,omitempty"`
	// Credits name the outside footage in the video (clips imported from YouTube,
	// TikTok or X): "Includes footage from @handle on YouTube: <url>", footage
	// inside nested cuts included, and pictures from the web.
	Credits []DeliveryCredit `json:"credits,omitempty"`
	// Resources are the pictures, logos, PDF pages and fonts the video shows,
	// pinned by hash, with where each came from (D79).
	Resources []DeliveryResource `json:"resources,omitempty"`
	// NestedCuts are other projects' cuts played inside this one (D84).
	NestedCuts []DeliveryNestedCut `json:"nestedCuts,omitempty"`
	// Narration says how the voiceover was made; AIVoice marks synthetic speech.
	Narration *DeliveryNarration `json:"narration,omitempty"`
	// Generated lists the AI-generated clips the video shows, with what each
	// place asks people to disclose (D127).
	Generated *DeliveryGenerated `json:"generated,omitempty"`
	// ChangedWords lists every place the video says words someone never
	// said: words changed in the speaker's own voice copy, with their consent
	// (D311), what was said and what is said now.
	ChangedWords []DeliveryChangedWords `json:"changedWords,omitempty"`
	// Captions are the captions files next to the video, where the place it
	// goes takes them (D148); "Update the captions file" rewrites them with
	// later text fixes (deliver.captions).
	Captions *DeliveryCaptions `json:"captions,omitempty"`
	// Notes are the version's title, description, chapters and show notes
	// (D237), also in NotesFile next to the video (Markdown).
	Notes *models.DeliveryNotes `json:"notes,omitempty"`
	// Receipt is the project's receipt in short, with what this version
	// costs at cost (D240).
	Receipt *DeliveryReceipt `json:"receipt,omitempty"`
	// Gate is the ship gate when it went out (D298, D299): on or off, what
	// held it, and the person's note that sent it out anyway.
	Gate       *DeliveryGate    `json:"gate,omitempty"`
	NotesFile  string           `json:"notesFile,omitempty" jsonschema:"description=The Markdown file next to the video with its title\\, description\\, chapters and show notes."`
	QA         *models.RenderQA `json:"qa,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
	ExportedAt time.Time        `json:"exportedAt"`
	ExportedBy models.Actor     `json:"exportedBy"`
	Machine    string           `json:"machine"`
	Generator  string           `json:"generator"`
}

// DeliveryCaptions are a delivered video's captions files (D148).
type DeliveryCaptions struct {
	Files []string `json:"files" jsonschema:"description=File names next to the video (SubRip and WebVTT)."`
	// Words: "version" (the words the video was made with) or "now" (updated
	// with the text fixes made since).
	Words string `json:"words" jsonschema:"enum=version,enum=now"`
	// BurnedIn: the video draws captions too (those keep the version's words).
	BurnedIn  bool       `json:"burnedIn"`
	Fixes     int        `json:"fixes,omitempty" jsonschema:"description=Text fixes made since the version that the files have."`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// Translations are the captions files in other languages (D238).
	Translations []DeliveryTranslation `json:"translations,omitempty"`
}

// DeliveryTranslation is a delivered video's captions in another language (D238).
type DeliveryTranslation struct {
	Language string   `json:"language" jsonschema:"description=Its code (es\\, pt-BR)."`
	Name     string   `json:"name"`
	Files    []string `json:"files" jsonschema:"description=File names next to the video (SubRip and WebVTT)."`
	// MachineTranslated: Claude translated it (the WebVTT file says so too).
	MachineTranslated bool   `json:"machineTranslated"`
	Notice            string `json:"notice,omitempty" jsonschema:"description=What to say about it: machine-translated\\, check it before publishing."`
	Lines             int    `json:"lines"`
	// Untranslated lines are in the original language; Outdated ones changed
	// in the transcript since they were translated (their old translation is used).
	Untranslated int `json:"untranslated,omitempty"`
	Outdated     int `json:"outdated,omitempty"`
}

// DeliveryGenerated lists the AI-generated clips a delivered video shows
// and what each place asks people to disclose when they post it (D127).
type DeliveryGenerated struct {
	Notice    string  `json:"notice" jsonschema:"description=A sentence for the description: the video contains AI-generated footage."`
	Seconds   float64 `json:"seconds" jsonschema:"description=How long generated footage is on screen."`
	Likeness  bool    `json:"likeness" jsonschema:"description=It shows a real person's likeness made with AI (with their consent)."`
	Watermark string  `json:"watermark,omitempty" jsonschema:"description=The provider's invisible watermark in the generated clips."`
	// Presenter: a presenter made with AI reads lines on camera (D277).
	Presenter bool                    `json:"presenter,omitempty"`
	Clips     []DeliveryGeneratedClip `json:"clips"`
	Checklist []models.DisclosureItem `json:"checklist"`
}

// DeliveryChangedWords is one change of spoken words a delivered video plays (D311).
type DeliveryChangedWords struct {
	ChangeID string `json:"changeId"`
	AssetID  string `json:"assetId" jsonschema:"description=The recording whose words changed."`
	// At is where it is heard in the video (seconds); From and To the stretch of the recording.
	At     float64 `json:"at"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
	Who    string  `json:"who"`
	Before string  `json:"before"`
	Words  string  `json:"words"`
	// Sound is the new sound's clip, with how it was made.
	Sound        string `json:"sound"`
	GenerationID string `json:"generationId,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	// In: a cut this one plays has them (a nested cut).
	In bool `json:"in,omitempty"`
	// Withdrawn says why the new words may no longer be heard (their consent
	// is no longer in force; such a version is held back from going out).
	Withdrawn string `json:"withdrawn,omitempty"`
	// Mouth: the mouth is matched to the new words too; MouthWithdrawn says
	// why it may no longer be shown.
	Mouth          bool   `json:"mouth,omitempty"`
	MouthWithdrawn string `json:"mouthWithdrawn,omitempty"`
}

// DeliveryGeneratedClip is one AI-generated clip a delivered video shows.
type DeliveryGeneratedClip struct {
	AssetID string `json:"assetId"`
	// Kind is what the AI service made (D226): video, or picture (a resource
	// the cut pins: ResourceID), voice, sound-effect, music, voice-change, dubbing.
	Kind         string `json:"kind,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	Title        string `json:"title"`
	GenerationID string `json:"generationId"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Look         string `json:"look,omitempty"`
	Likeness     bool   `json:"likeness,omitempty"`
	// Presenter names who reads the lines in a presenter clip (D277), the
	// consent a person's presenter relies on, and its watermark ("none" when
	// the service documents none).
	Presenter          string   `json:"presenter,omitempty"`
	Consent            string   `json:"consent,omitempty"`
	PresenterWatermark string   `json:"presenterWatermark,omitempty"`
	SHA256             string   `json:"sha256" jsonschema:"description=The provider's original file\\, kept as it came."`
	UsedAs             []string `json:"usedAs"`
	Seconds            float64  `json:"seconds"`
}

// DeliverySource is a source asset used by the cut.
type DeliverySource struct {
	AssetID string   `json:"assetId"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	SHA256  string   `json:"sha256"`
	UsedAs  []string `json:"usedAs"`
}

// creditLines are a delivery's credit lines, each once, in order.
func creditLines(cs []DeliveryCredit) []string {
	var out []string
	for _, c := range cs {
		if c.Credit != "" && !slices.Contains(out, c.Credit) {
			out = append(out, c.Credit)
		}
	}
	return out
}

// abilityCredits reports a credit an ability's licence asks for (a notes file
// is written for it even when the version has no notes, D283).
func abilityCredits(cs []DeliveryCredit) bool {
	for _, c := range cs {
		if c.Credit != "" && c.Platform == models.PlatformAbility {
			return true
		}
	}
	return false
}

// CreditsMarkdown is the notes file of a version with no delivery notes whose
// outside files ask for credit (D283).
func CreditsMarkdown(cut string, number int, credits []string) string {
	return fmt.Sprintf("# %s\n\nCredits for version %d of %s, ready to paste into a description.\n\n## Credits\n\n- %s\n", cut, number, cut, strings.Join(credits, "\n- "))
}

// assetCredit is a clip's credit line: what an ability said about it (sound
// said as sound, D283), else where it came from.
func assetCredit(as models.Asset) string {
	if s := as.Source; s.IsAbility() {
		what := "footage"
		if as.Kind == "audio" {
			what = "sound"
		}
		return s.MadeWith.CreditLine(what)
	}
	return as.Source.Credit()
}

// resourceWhat says a resource's kind in a credit line.
func resourceWhat(kind string) string {
	switch kind {
	case "pdf":
		return "a document"
	case "font":
		return "a font"
	}
	return "a picture"
}

// DeliveryCredit is outside footage a delivered video uses, with where it came from.
type DeliveryCredit struct {
	AssetID string `json:"assetId,omitempty"`
	// ResourceID is set for a picture or font from the web (no AssetID).
	ResourceID string `json:"resourceId,omitempty"`
	Credit     string `json:"credit" jsonschema:"description=The credit line in plain words."`
	Platform   string `json:"platform"`
	URL        string `json:"url"`
	Uploader   string `json:"uploader,omitempty"`
	Handle     string `json:"handle,omitempty"`
	Title      string `json:"title,omitempty"`
	UploadDate string `json:"uploadDate,omitempty"`
	License    string `json:"license,omitempty" jsonschema:"description=The license the site states\\, if any."`
}

// DeliveryResource is a resource version a delivered video shows.
type DeliveryResource struct {
	Name       string `json:"name" jsonschema:"description=Its short name in the cut."`
	ResourceID string `json:"resourceId"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	// From says where it came from, in plain words ("acme-logo.png", "the linked folder Brand assets", "example.com").
	From    string `json:"from"`
	URL     string `json:"url,omitempty"`
	License string `json:"license,omitempty"`
	Author  string `json:"author,omitempty"`
	// In names the project whose nested cut shows it (empty: this cut pins it).
	In string `json:"in,omitempty" jsonschema:"description=The project whose cut (played inside this one) shows it; empty when this cut pins it."`
	// Provenance is exactly where it came from: repo@commit:path:lines for a
	// code slide, a repository's commit, a page and the day of its snapshot.
	Provenance string `json:"provenance,omitempty"`
}

// DeliveryNestedCut is another project's cut a delivered video plays.
type DeliveryNestedCut struct {
	ClipID         string `json:"clipId"`
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
	RevisionID     string `json:"revisionId"`
	RevisionNumber int    `json:"revisionNumber"`
	Title          string `json:"title,omitempty"`
	// In names the project whose cut plays it when it is nested deeper.
	In string `json:"in,omitempty" jsonschema:"description=The project whose nested cut plays it (deeper nesting); empty when this cut plays it."`
}

// DeliveryNarration describes the voiceover in a delivered file.
type DeliveryNarration struct {
	Lines   int             `json:"lines"`
	AIVoice bool            `json:"aiVoice"`          // contains synthetic speech (TTS or a voice clone)
	Notice  string          `json:"notice,omitempty"` // plain disclosure text, e.g. for a description
	Sources map[string]int  `json:"sources"`          // lines by how their audio was made: recorded, imported, tts, scratch
	Voices  []DeliveryVoice `json:"voices,omitempty"`
}

// DeliveryVoice is one synthetic voice used in the narration.
type DeliveryVoice struct {
	Provider string `json:"provider"`
	Voice    string `json:"voice"`
	Model    string `json:"model,omitempty"`
	Lines    int    `json:"lines"`
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func deliveryTakes(ctx context.Context, a *app.App, s *editspec.Spec) map[string]models.VoiceTake {
	out := map[string]models.VoiceTake{}
	if s.Voiceover == nil {
		return out
	}
	var ids []string
	for _, b := range s.Voiceover.Beats {
		if b.Take != nil && b.Take.TakeID != "" {
			ids = append(ids, b.Take.TakeID)
		}
	}
	if len(ids) == 0 {
		return out
	}
	var ts []models.VoiceTake
	_ = a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &ts)
	for _, t := range ts {
		out[t.ID] = t
	}
	return out
}

// DeliveryRights records the rights of a music (or other third-party audio) track.
type DeliveryRights struct {
	TrackID     string `json:"trackId"`
	AssetID     string `json:"assetId"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	Source      string `json:"source,omitempty"`
	Recorded    bool   `json:"recorded"`
}

// DeliveryClaim is a factual on-screen claim (data card) and its stated source.
type DeliveryClaim struct {
	OverlayID string `json:"overlayId"`
	Value     string `json:"value"`
	Label     string `json:"label"`
	Source    string `json:"source"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string, n int) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > n {
		s = strings.Trim(s[:n], "-")
	}
	return s
}

type deliveryItem struct {
	rev    models.Revision
	proj   models.Project
	render models.Render
	base   string
	assets map[string]models.Asset
	takes  map[string]models.VoiceTake // narration takes by ID (voice names for the sidecar)
	// extras: the resources the cut pins and the cuts it nests (D79, D84).
	extras *RenderExtras
	// said: the changed words it plays, checked once (heard).
	said     []SaidHeard
	saidDone bool
}

func init() {
	Def("deliver.export", "Copy finished renders of revisions to a local folder with descriptive names and a JSON sidecar each (hash-verified, never overwrites, never uploads)", Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *DeliverIn) (*DeliverOut, error) {
		if len(in.RevisionIDs) == 0 {
			return nil, invalid("Choose at least one version to export.")
		}
		if strings.TrimSpace(in.Out) == "" {
			return nil, invalid("Choose a folder to export to.")
		}
		quality := in.Quality
		if quality == "" {
			quality = "final"
		}
		if quality != "final" && quality != "review" {
			return nil, invalid("Export final renders or review copies; “%s” isn't one of them.", quality)
		}
		for _, p := range in.Presets {
			if _, ok := editspec.Presets[p]; !ok {
				return nil, invalid("“%s” isn't a video shape MetaRush renders: choose vertical-1080p, landscape-1080p or square-1080.", p)
			}
		}
		dir, err := filepath.Abs(in.Out)
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't use that folder.").WithDetails(map[string]any{"out": in.Out})
		}
		// 1. Check everything before writing anything.
		var items []*deliveryItem
		var refused, missing []string // the technical detail (Details)
		// What people read: the versions refused or without a render.
		var rejectedVs, draftVs, unrendered []string
		var draftID, unrenderedID, unrenderedPreset string
		seen := map[string]bool{}
		for _, id := range in.RevisionIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			rev, proj, err := getRevision(ctx, a, id)
			if err != nil {
				return nil, err
			}
			// What may leave (D290): delivered into a folder a sync service uploads.
			if err := deliveryMayLeave(ctx, a, dir, deliveryKinds, revisionTouches(ctx, a, rev), "a delivered video of “"+proj.Name+"”"); err != nil {
				return nil, err
			}
			switch {
			case rev.Approval.State == models.ApprovalRejected:
				refused = append(refused, fmt.Sprintf("r%d %s was rejected", rev.Number, rev.ID))
				rejectedVs = append(rejectedVs, strconv.Itoa(rev.Number))
				continue
			case rev.Approval.State != models.ApprovalApproved && !in.AllowDraft:
				refused = append(refused, fmt.Sprintf("r%d %s is %s (not approved)", rev.Number, rev.ID, orState(rev.Approval.State)))
				draftVs = append(draftVs, strconv.Itoa(rev.Number))
				if draftID == "" {
					draftID = rev.ID
				}
				continue
			}
			var renders []models.Render
			if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "state": models.RenderAvailable, "quality": quality}, &renders,
				options.Find().SetSort(bson.D{{Key: "completedAt", Value: -1}})); err != nil {
				return nil, err
			}
			byPreset := map[string]models.Render{}
			for _, r := range renders {
				if r.Output == nil || r.SpecHash != rev.SpecHash {
					continue
				}
				if _, ok := byPreset[r.Preset]; !ok {
					byPreset[r.Preset] = r
				}
			}
			want := in.Presets
			if len(want) == 0 {
				for p := range byPreset {
					want = append(want, p)
				}
				sort.Strings(want)
			}
			if len(want) == 0 {
				missing = append(missing, fmt.Sprintf("r%d %s has no %s render", rev.Number, rev.ID, quality))
				unrendered = append(unrendered, fmt.Sprintf("version %d has no %s render", rev.Number, quality))
				if unrenderedID == "" {
					unrenderedID = rev.ID
				}
				continue
			}
			assets, err := deliveryAssets(ctx, a, &rev.Spec)
			if err != nil {
				return nil, err
			}
			for _, p := range want {
				r, ok := byPreset[p]
				if !ok {
					missing = append(missing, fmt.Sprintf("r%d %s has no %s %s render", rev.Number, rev.ID, quality, p))
					unrendered = append(unrendered, fmt.Sprintf("version %d has no %s %s render", rev.Number, quality, editspec.PresetLabel(p)))
					if unrenderedID == "" {
						unrenderedID, unrenderedPreset = rev.ID, p
					}
					continue
				}
				title := rev.Spec.Title
				if title == "" {
					title = "cut"
				}
				base := fmt.Sprintf("%s_%s_r%d_%s_%ds", orSlug(slug(proj.Name, 40), "project"), orSlug(slug(title, 48), "cut"), rev.Number, p, int(math.Round(r.Output.Duration)))
				if quality == "review" {
					base += "_review"
				}
				extras, _ := LoadRenderExtras(ctx, a, &rev.Spec)
				items = append(items, &deliveryItem{rev: *rev, proj: *proj, render: r, base: base, assets: assets, takes: deliveryTakes(ctx, a, &rev.Spec), extras: extras})
			}
		}
		if len(refused) > 0 {
			return nil, notApprovedError(rejectedVs, draftVs, draftID).WithDetails(map[string]any{"refused": refused})
		}
		if len(missing) > 0 {
			cmd := "metarush render submit --revision " + unrenderedID + " --quality " + quality + " --wait"
			if unrenderedPreset != "" {
				cmd += " --preset " + unrenderedPreset
			}
			return nil, apperr.New(apperr.NotFound, "%s, so there's nothing to export yet.", sentenceStart(andList(unrendered))).WithDetails(map[string]any{"missing": missing}).
				WithHint("Render it first (on the project's Versions tab, or ask Claude Code), then export it.").WithCommand(cmd)
		}
		// The ship gate (D298): the quality check of what goes out and the
		// sign-offs, unless a person sent it out anyway (D299).
		gates := map[string]*ShipGateOut{}
		var held []*ShipGateOut
		for _, it := range items {
			if gates[it.rev.ID] != nil {
				continue
			}
			g, err := ShipGate(ctx, a, &it.proj, &it.rev, quality, in.Presets)
			if err != nil {
				return nil, err
			}
			gates[it.rev.ID] = g
			if !g.letsOut() {
				held = append(held, g)
			}
		}
		if len(held) > 0 {
			return nil, heldError(held, "delivery")
		}
		for _, it := range items { // the changed words as the gate checked them (one plan per version)
			it.said, it.saidDone = gates[it.rev.ID].heard, true
		}
		var exists []string
		names := map[string]bool{}
		langs := map[string][]string{} // a project's captions languages (D238)
		for _, it := range items {
			if _, ok := langs[it.proj.ID]; !ok {
				l, err := projectLanguages(ctx, a, it.proj.ID)
				if err != nil {
					return nil, err
				}
				langs[it.proj.ID] = l
			}
			exts := []string{".mp4", ".json"}
			if transcripts.CaptionFileAllowed(it.rev.Spec.Output.Platform) {
				exts = append(exts, ".srt", ".vtt")
				for _, l := range langs[it.proj.ID] {
					exts = append(exts, "."+l+".srt", "."+l+".vtt")
				}
			}
			if !it.rev.DeliveryNotes.Empty() || gates[it.rev.ID].record().SentAnyway != nil {
				exts = append(exts, ".notes.md")
			}
			for _, ext := range exts {
				p := filepath.Join(dir, it.base+ext)
				if names[p] {
					return nil, apperr.New(apperr.Conflict, "Two of these videos would get the same file name, %s: export them one at a time.", filepath.Base(p)).WithDetails(map[string]any{"path": p})
				}
				names[p] = true
				if _, err := os.Lstat(p); err == nil {
					exists = append(exists, p)
				}
			}
		}
		if len(exists) > 0 {
			return nil, alreadyThere(exists, dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the folder %s.", filepath.Base(dir)).WithDetails(map[string]any{"folder": dir})
		}
		// 2. Copy, verify, write sidecars.
		out := &DeliverOut{Dir: dir, Files: []DeliverFile{}, Warnings: []string{}}
		notes, creditsOnly := false, false
		for _, it := range items {
			o := it.render.Output
			src, err := a.Cache.Ensure(ctx, a.Media, o.StorageKey, o.SHA256, o.Size, nil)
			if err != nil {
				return out, apperr.Wrap(apperr.Storage, err, "The %s couldn't be copied to this machine, so it wasn't exported.", strings.ToLower(models.RenderLabel(it.render.Quality, it.rev.Number))).
					WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"renderId": it.render.ID})
			}
			dst := filepath.Join(dir, it.base+".mp4")
			if err := copyVerified(src, dst, o.SHA256, "the render"); err != nil {
				return out, err
			}
			sc := buildSidecar(a, it, filepath.Base(dst))
			// Captions files where the place takes them, with the words the video
			// was made with (D148).
			var capFiles []string
			if transcripts.CaptionFileAllowed(it.rev.Spec.Output.Platform) {
				cues, _, err := captionCues(ctx, a, &it.rev, true)
				if err != nil {
					return out, err
				}
				if len(cues) > 0 {
					srt, vtt := filepath.Join(dir, it.base+".srt"), filepath.Join(dir, it.base+".vtt")
					if err := writeExclusive(srt, []byte(transcripts.SRT(cues))); err != nil {
						return out, err
					}
					if err := writeExclusive(vtt, []byte(transcripts.VTTWithNote(cues, changedNote(it.heard(a))))); err != nil {
						return out, err
					}
					capFiles = []string{srt, vtt}
					sc.Captions = &DeliveryCaptions{Files: []string{filepath.Base(srt), filepath.Base(vtt)}, Words: "version", BurnedIn: burnedIn(&it.rev)}
					// The same lines in each language they are translated into (D238).
					from := captionsFrom(ctx, a, &it.rev)
					for _, l := range langs[it.proj.ID] {
						tf, err := translatedCaptions(ctx, a, &it.rev, l, true)
						if err != nil {
							return out, err
						}
						if tf == nil {
							continue
						}
						lsrt, lvtt := filepath.Join(dir, it.base+"."+l+".srt"), filepath.Join(dir, it.base+"."+l+".vtt")
						if err := writeExclusive(lsrt, []byte(tf.SRT)); err != nil {
							return out, err
						}
						if err := writeExclusive(lvtt, []byte(tf.VTT)); err != nil {
							return out, err
						}
						capFiles = append(capFiles, lsrt, lvtt)
						d := translationRecord(ctx, a, &it.rev, l, []string{filepath.Base(lsrt), filepath.Base(lvtt)}, tf.Lines)
						sc.Captions.Translations = append(sc.Captions.Translations, d)
						sc.Warnings = append(sc.Warnings, translationWarnings(filepath.Base(dst), d, from)...)
					}
				}
			}
			// The receipt in short (D240): what the version costs at cost, and the project's requests.
			if DeliveryReceiptFor != nil {
				sc.Receipt = DeliveryReceiptFor(ctx, a, &it.proj, &it.rev)
			}
			// The ship gate (D298, D299): what held it, and who sent it out anyway.
			sc.Gate = gates[it.rev.ID].record()
			anyway := sentAnywayMarkdown(sc.Gate)
			// The version's delivery notes (D237): in the record and as a file to
			// paste from, with the credits its outside files ask for (D283) — a
			// file of its own when only an ability's licences ask for credit, or
			// when a person sent it out anyway (D299).
			if n, credits := it.rev.DeliveryNotes, creditLines(sc.Credits); !n.Empty() || abilityCredits(sc.Credits) || anyway != "" {
				nf := filepath.Join(dir, it.base+".notes.md")
				md := fmt.Sprintf("# %s\n\nNotes on version %d of %s.\n", cutTitle(&it.rev, &it.proj), it.rev.Number, cutTitle(&it.rev, &it.proj))
				if abilityCredits(sc.Credits) || (len(credits) > 0 && n.Empty()) {
					md = CreditsMarkdown(cutTitle(&it.rev, &it.proj), it.rev.Number, credits)
				}
				if !n.Empty() {
					md = NotesMarkdown(cutTitle(&it.rev, &it.proj), it.rev.Number, n)
					if len(credits) > 0 {
						md += "\n## Credits\n\n" + "- " + strings.Join(credits, "\n- ") + "\n"
					}
					sc.Notes = n
				}
				if anyway != "" {
					md += "\n" + anyway
				}
				if err := writeExclusive(nf, []byte(md)); err != nil {
					return out, err
				}
				sc.NotesFile = filepath.Base(nf)
				notes, creditsOnly = notes || sc.Notes != nil, creditsOnly || (sc.Notes == nil && len(creditLines(sc.Credits)) > 0)
			}
			out.Warnings = append(out.Warnings, sc.Warnings...)
			if sc.Generated != nil && (out.Disclosure == nil || (sc.Generated.Likeness && !out.Disclosure.Likeness)) {
				out.Disclosure = sc.Generated
			}
			for _, c := range sc.Credits {
				if !slices.Contains(out.Credits, c.Credit) {
					out.Credits = append(out.Credits, c.Credit)
				}
			}
			b, _ := json.MarshalIndent(sc, "", "  ")
			side := filepath.Join(dir, it.base+".json")
			if err := writeExclusive(side, append(b, '\n')); err != nil {
				return out, err
			}
			f := DeliverFile{RevisionID: it.rev.ID, RevisionNumber: it.rev.Number, Title: it.rev.Spec.Title, Approval: orState(it.rev.Approval.State), RenderID: it.render.ID,
				Preset: it.render.Preset, Quality: it.render.Quality, Duration: o.Duration, Path: dst, Sidecar: side, Captions: capFiles, Size: o.Size, SHA256: o.SHA256}
			if it.render.QA != nil {
				f.QAVerdict = it.render.QA.Verdict
			}
			if sc.Gate != nil {
				f.SentAnyway = sc.Gate.SentAnyway
			}
			out.Files = append(out.Files, f)
		}
		files := make([]string, 0, len(out.Files))
		for _, f := range out.Files {
			files = append(files, filepath.Base(f.Path))
		}
		projectID := ""
		if len(items) > 0 {
			projectID = items[0].proj.ID
		}
		// Sign-offs still waiting (D166): a reminder, never a lock.
		remindedFor := map[string]bool{}
		for _, it := range items {
			k := it.proj.ID + "/" + it.rev.Branch
			if remindedFor[k] {
				continue
			}
			remindedFor[k] = true
			if open := OpenSignoffs(ctx, a, &it.proj, it.rev.Branch); len(open) > 0 {
				out.Signoffs = append(out.Signoffs, open...)
				out.Warnings = append(out.Warnings, it.proj.Name+": "+SignoffReminder(open))
			}
		}
		// What went out past the gate, with the notes that let it (D299), and
		// the folder: it is a client's delivery folder from now on (D298).
		recorded := map[string]bool{}
		for _, it := range items {
			if g := gates[it.rev.ID]; g != nil && !recorded[it.rev.ID] && len(g.record().SentAnyway) > 0 {
				recorded[it.rev.ID] = true
				if err := recordSentOut(ctx, a, it.rev.ID, g.record().SentAnyway, "delivery", filepath.Base(dir)); err != nil {
					a.Audit(ctx, "delivery.sentOut.failed", "revision", it.rev.ID, it.proj.ID, map[string]any{"error": err.Error()}) // the log keeps it
					out.Warnings = append(out.Warnings, fmt.Sprintf(sentOutNote, it.rev.Number))
				}
			}
		}
		if len(out.Files) > 0 {
			if err := RememberDeliveryFolder(a, dir); err != nil {
				a.Audit(ctx, "delivery.folder.unremembered", "workspace", a.WorkspaceID(), projectID, map[string]any{"error": err.Error()})
				out.Warnings = append(out.Warnings, "MetaRush couldn't remember this folder as a client's delivery folder on this computer, so a timeline exported into it later is held only while the delivered videos' records are there.")
			}
		}
		a.Audit(ctx, "deliver.export", "revision", strings.Join(in.RevisionIDs, ","), projectID, map[string]any{"dir": dir, "files": files, "allowDraft": in.AllowDraft})
		out.Note = "Exported to that folder and checked against the renders; nothing was uploaded. Next to each video, a .json file records its version, approval, footage and music rights."
		outside := false
		for _, c := range out.Credits {
			outside = outside || !strings.HasPrefix(c, "Includes AI-generated footage")
		}
		if outside {
			out.Note += " These videos include outside footage, and those .json files credit it."
		}
		if out.Disclosure != nil {
			out.Note += " They include AI-generated footage: say so when you post them. Each .json file lists what YouTube, TikTok, Instagram and Facebook ask for."
		}
		switch {
		case notes:
			out.Note += " The .notes.md files hold each version's title, description, chapters and show notes, ready to paste, with the credits its outside files ask for."
		case creditsOnly:
			out.Note += " The .notes.md files hold the credits its outside files ask for, ready to paste into a description."
		}
		for _, f := range out.Files {
			if len(f.SentAnyway) > 0 {
				var names []string
				for _, o := range f.SentAnyway {
					if n := actorName(o.By); !slices.Contains(names, n) {
						names = append(names, n)
					}
				}
				out.Note += fmt.Sprintf(" Version %d went out although something held it: %s sent it out anyway, and its .json and .notes.md files say why.", f.RevisionNumber, andList(names))
				break
			}
		}
		for _, f := range out.Files {
			if len(f.Captions) > 0 {
				out.Note += " The .srt and .vtt files are its captions, for places that take a captions file (YouTube, Facebook, LinkedIn, Vimeo); later text fixes reach them without a new render."
				break
			}
		}
		for _, f := range out.Files {
			if len(f.Captions) > 2 {
				out.Note += " Captions in other languages carry the language's code in their names (.es.srt); the ones Claude translated are marked as machine-translated."
				break
			}
		}
		return out, nil
	})
}

// notApprovedError says which versions can't be exported and what to do.
func notApprovedError(rejected, drafts []string, draftID string) *apperr.Error {
	versions := func(nums []string) string {
		if len(nums) == 1 {
			return "version " + nums[0]
		}
		return "versions " + andList(nums)
	}
	var parts []string
	if len(rejected) > 0 {
		verb := " was rejected"
		if len(rejected) > 1 {
			verb = " were rejected"
		}
		parts = append(parts, versions(rejected)+verb)
	}
	if len(drafts) > 0 {
		verb := " isn't approved yet"
		if len(drafts) > 1 {
			verb = " aren't approved yet"
		}
		parts = append(parts, versions(drafts)+verb)
	}
	e := apperr.New(apperr.Conflict, "%s.", sentenceStart(strings.Join(parts, " and ")))
	if len(drafts) == 0 {
		return e.WithHint("A rejected version is never exported: make a new version instead.")
	}
	hint := "Open " + versions(drafts) + " and approve it (only people can approve), or ask to export it as a draft."
	if len(drafts) > 1 {
		hint = "Open " + versions(drafts) + " and approve them (only people can approve), or ask to export them as drafts."
	}
	if len(rejected) > 0 {
		hint += " A rejected version is never exported."
	}
	return e.WithHint(hint).WithCommand("metarush revision approve " + draftID)
}

// alreadyThere refuses to overwrite files: it names them by file name (the
// full paths are in Details).
func alreadyThere(paths []string, dir string) *apperr.Error {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = filepath.Base(p)
	}
	them := "it"
	if len(names) > 1 {
		them = "them"
	}
	return apperr.New(apperr.Conflict, "That folder already has %s, and MetaRush never overwrites files.", andList(names)).
		WithHint("Choose another folder, or move " + them + " first.").WithDetails(map[string]any{"existing": paths, "folder": dir})
}

// andList joins words as people list them: a, b and c.
func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// sentenceStart starts a sentence with a capital letter.
func sentenceStart(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orState(s string) string {
	if s == "" {
		return models.ApprovalDraft
	}
	return s
}

func orSlug(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func deliveryAssets(ctx context.Context, a *app.App, s *editspec.Spec) (map[string]models.Asset, error) {
	out := map[string]models.Asset{}
	idsList := specAssetIDs(s)
	if len(idsList) == 0 {
		return out, nil
	}
	var as []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}}, &as); err != nil {
		return nil, err
	}
	for _, x := range as {
		out[x.ID] = x
	}
	return out, nil
}

func buildSidecar(a *app.App, it *deliveryItem, file string) DeliverySidecar {
	var sc DeliverySidecar
	sc.Format, sc.File, sc.Title = "metarush.delivery/1", file, it.rev.Spec.Title
	sc.Project.ID, sc.Project.Name = it.proj.ID, it.proj.Name
	r := &sc.Revision
	r.ID, r.Number, r.Branch, r.SpecHash, r.Approval = it.rev.ID, it.rev.Number, it.rev.Branch, it.rev.SpecHash, it.rev.Approval
	r.Instruction, r.Rationale, r.Notes = it.rev.Instruction, it.rev.Rationale, it.rev.Spec.Notes
	o := it.render.Output
	rd := &sc.Render
	rd.ID, rd.Preset, rd.Quality = it.render.ID, it.render.Preset, it.render.Quality
	rd.Width, rd.Height, rd.FPS, rd.Duration, rd.SHA256, rd.Size = o.Width, o.Height, o.FPS, o.Duration, o.SHA256, o.Size
	rd.RenderedAt, rd.RendererVersion, rd.TemplateVersions = it.render.CompletedAt, it.render.RendererVersion, it.render.TemplateVersions
	used := map[string][]string{}
	var order []string
	use := func(id, as string) {
		if _, ok := used[id]; !ok {
			order = append(order, id)
		}
		used[id] = append(used[id], as)
	}
	for _, c := range it.rev.Spec.Clips {
		if editspec.IsNested(c.AssetID) {
			continue // a nested cut: listed under nestedCuts
		}
		use(c.AssetID, "clip:"+c.ID)
	}
	sc.MusicRights = []DeliveryRights{}
	for _, t := range it.rev.Spec.Audio {
		use(t.AssetID, "audio:"+t.ID+"("+t.Role+")")
		if t.Role == editspec.RoleVoiceover || t.Role == editspec.RoleDialogue {
			continue
		}
		dr := DeliveryRights{TrackID: t.ID, AssetID: t.AssetID, Name: it.assets[t.AssetID].Name, Role: t.Role}
		if t.Rights != nil && (t.Rights.License != "" || t.Rights.Source != "" || t.Rights.Attribution != "") {
			dr.License, dr.Attribution, dr.Source, dr.Recorded = t.Rights.License, t.Rights.Attribution, t.Rights.Source, true
		} else if r := it.assets[t.AssetID].Rights; r != nil && r.License != "" { // recorded on the file (asset rights / to-do answer)
			dr.License, dr.Attribution, dr.Source, dr.Recorded = r.License, r.Attribution, r.Source, true
		} else if terms, made := aiTerms(a, it.assets[t.AssetID]); terms != nil { // made by an AI service: its licence travels with it (D225)
			dr.License, dr.Source, dr.Recorded = terms.Licence, made, true
			if as := it.assets[t.AssetID]; terms.Broadcast != "yes" {
				sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: the licence of “%s” (%s) may not cover every use: %s", file, as.DisplayTitle(), made, terms.Licence))
			}
		} else {
			as := it.assets[t.AssetID]
			what, title := map[string]string{editspec.RoleMusic: "music"}[t.Role], as.DisplayTitle()
			if what == "" {
				what = "track"
			}
			if title == "" {
				title = t.ID
			}
			sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: nobody has said where the %s “%s” comes from or whether you may use it.", file, what, title))
		}
		sc.MusicRights = append(sc.MusicRights, dr)
	}
	sc.Sources = []DeliverySource{}
	sc.Generated = aiPictures(a, it, generatedIn(it, order, used))
	sc.ChangedWords = changedWordsIn(a, it)
	sc.Generated = withChangedWords(it, sc.Generated, sc.ChangedWords)
	for _, id := range order {
		as := it.assets[id]
		sc.Sources = append(sc.Sources, DeliverySource{AssetID: id, Name: as.Name, Kind: as.Kind, SHA256: as.SHA256, UsedAs: used[id]})
		if s := as.Source; s != nil {
			sc.Credits = append(sc.Credits, DeliveryCredit{AssetID: id, Credit: assetCredit(as), Platform: s.Platform, URL: s.URL, Uploader: s.Uploader, Handle: s.Handle,
				Title: s.Title, UploadDate: s.UploadDate, License: s.License})
		}
	}
	// Nested cuts at any depth, and the outside footage they play (credited
	// like this cut's).
	credited := map[string]bool{}
	for _, c := range sc.Credits {
		credited[c.AssetID] = true
	}
	for _, u := range it.extras.NestedCuts(&it.rev.Spec) {
		ns := u.Source
		sc.NestedCuts = append(sc.NestedCuts, DeliveryNestedCut{ClipID: u.ClipID, ProjectID: ns.ProjectID, ProjectName: ns.ProjectName, RevisionID: ns.RevisionID,
			RevisionNumber: ns.Number, Title: ns.Title, In: u.In})
		for _, id := range editspec.AssetIDs(ns.Spec) {
			as, ok := it.extras.Asset(id)
			if !ok || as.Source == nil || credited[id] {
				continue
			}
			credited[id] = true
			s := as.Source
			sc.Credits = append(sc.Credits, DeliveryCredit{AssetID: id, Credit: assetCredit(*as), Platform: s.Platform, URL: s.URL, Uploader: s.Uploader, Handle: s.Handle,
				Title: s.Title, UploadDate: s.UploadDate, License: s.License})
		}
	}
	// Resources (nested cuts' included): each pinned version, where it came
	// from, and a credit for pictures from the web.
	for _, shown := range it.extras.ShownResources(&it.rev.Spec) {
		r, pin := shown.Doc, shown.Pin
		dr := DeliveryResource{Name: shown.Name, ResourceID: r.ID, Title: r.Title, Kind: r.Kind, SHA256: pin.SHA256, License: r.License, In: shown.In}
		if v := r.Version(pin.SHA256); v != nil {
			src := v.Source
			dr.URL, dr.Author, dr.Provenance = src.URL, src.Author, src.Provenance()
			if src.License != "" {
				dr.License = src.License
			}
			switch src.Kind {
			case models.FromFolder:
				dr.From = "a linked folder (" + src.RelPath + ")"
			case models.FromURL, models.FromRepo:
				dr.From = firstNonEmpty(src.Site, src.URL)
				credit := fmt.Sprintf("Includes “%s” from %s", r.Title, dr.From)
				if src.Kind == models.FromRepo && src.File != "" {
					// A picture kept from a repository's folder (D216): the file at its commit.
					credit = fmt.Sprintf("Includes “%s” from %s", r.Title, src.Provenance())
				}
				if v.Site != nil {
					// A web page snapshot: the page and the day it was taken.
					credit = fmt.Sprintf("Includes a snapshot of %s (%s)", firstNonEmpty(v.Site.Title, dr.From), v.Site.TakenAt.UTC().Format("2 Jan 2006"))
				}
				if dr.License != "" {
					credit += " (" + dr.License + ")"
				}
				if src.URL != "" {
					credit += ": " + src.URL
				}
				sc.Credits = append(sc.Credits, DeliveryCredit{ResourceID: r.ID, Credit: credit, Platform: "web", URL: src.URL, Title: r.Title, License: dr.License})
			case models.FromProject:
				dr.From = "another project"
			case models.FromGenerated:
				dr.From = "made by " + firstNonEmpty(src.Tool, "MetaRush")
				if mw := src.MadeWith; mw != nil && mw.Ability != nil {
					// Brought in by an ability (D283): the credit its licence asks for.
					dr.From = "brought in with the ability " + mw.Ability.Ability
					if credit := mw.Ability.CreditLine(resourceWhat(r.Kind)); credit != "" {
						sc.Credits = append(sc.Credits, DeliveryCredit{ResourceID: r.ID, Credit: credit, Platform: models.PlatformAbility, URL: mw.Ability.Source, Title: r.Title,
							Uploader: mw.Ability.Author, License: firstNonEmpty(mw.Ability.Licence, dr.License)})
					}
				}
				if src.Commit != "" && src.URL != "" {
					// Made from a repository (D106): credit the code, exactly where it came from.
					dr.From = "made by " + firstNonEmpty(src.Tool, "MetaRush") + " from " + firstNonEmpty(src.Site, src.URL)
					what := "Includes code from "
					if src.MadeWith != nil && src.MadeWith.Kind != "code" {
						what = "Includes a picture made from "
					}
					credit := what + src.Provenance()
					if dr.License != "" {
						credit += " (" + dr.License + ")"
					}
					credit += ": " + src.URL
					sc.Credits = append(sc.Credits, DeliveryCredit{ResourceID: r.ID, Credit: credit, Platform: "repo", URL: src.URL, Title: r.Title, License: dr.License})
				}
			default:
				dr.From = firstNonEmpty(src.Name, "a file")
			}
		}
		sc.Resources = append(sc.Resources, dr)
	}
	for _, ov := range it.rev.Spec.Overlays {
		if ov.Template != editspec.TplDataCard {
			continue
		}
		var p editspec.DataCardParams
		_ = json.Unmarshal(ov.Params, &p)
		sc.Claims = append(sc.Claims, DeliveryClaim{OverlayID: ov.ID, Value: p.Value, Label: p.Label, Source: p.Source})
	}
	if v := it.rev.Spec.Voiceover; v != nil && len(v.Beats) > 0 {
		n := &DeliveryNarration{Lines: len(v.Beats), Sources: map[string]int{}}
		voices := map[string]*DeliveryVoice{}
		var order []string
		for _, b := range v.Beats {
			if b.Take == nil {
				n.Sources["none"]++
				continue
			}
			src := b.Take.Source
			if src == "" {
				src = "recorded"
			}
			n.Sources[src]++
			if b.Take.AIVoice {
				n.AIVoice = true
				t := it.takes[b.Take.TakeID]
				prov, name := firstNonEmpty(t.Provider, "unknown"), firstNonEmpty(t.VoiceName, t.VoiceID, "AI voice")
				key := prov + "\x00" + name
				if voices[key] == nil {
					voices[key] = &DeliveryVoice{Provider: prov, Voice: name, Model: t.ModelID}
					order = append(order, key)
				}
				voices[key].Lines++
			}
		}
		if c := v.Continuous; c != nil && c.AIVoice {
			n.AIVoice = true
		}
		for _, k := range order {
			n.Voices = append(n.Voices, *voices[k])
		}
		if n.AIVoice {
			n.Notice = "This video contains AI-generated narration."
			if k := n.Sources["scratch"]; k > 0 {
				sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: %s still use the free scratch voice, which is meant for checking the timing.", file, linesWord(k)))
			}
		}
		if k := n.Sources["none"]; k > 0 {
			sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: %s have no recording yet.", file, linesWord(k)))
		}
		sc.Narration = n
	}
	if it.rev.Approval.State != models.ApprovalApproved {
		sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: version %d isn't approved, and it was exported as a draft because you asked.", file, it.rev.Number))
	}
	if it.render.QA != nil && it.render.QA.Verdict == models.QAFail {
		problems := "problems"
		if it.render.QA.Errors == 1 {
			problems = "problem"
		}
		sc.Warnings = append(sc.Warnings, fmt.Sprintf("%s: the quality check found %d %s in this render.", file, it.render.QA.Errors, problems))
	}
	sc.QA = it.render.QA
	sc.ExportedAt, sc.ExportedBy, sc.Machine = time.Now().UTC(), a.Actor, a.Cfg.Installation.Name
	sc.Generator = "metarush " + version.Version
	return sc
}

// linesWord counts narration lines: 1 narration line, 3 narration lines.
func linesWord(n int) string {
	if n == 1 {
		return "1 narration line"
	}
	return strconv.Itoa(n) + " narration lines"
}

// copyVerified copies src to dst without ever replacing an existing file, hashing
// the bytes written; a mismatch removes the copy. what names the original in
// the error people read ("the render", "the footage").
func copyVerified(src, dst, wantSHA, what string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return apperr.Wrap(apperr.Conflict, err, "MetaRush couldn't write %s in that folder.", filepath.Base(dst)).WithDetails(map[string]any{"path": tmp})
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		os.Remove(tmp)
		return apperr.New(apperr.Integrity, "The copy of %s didn't match %s, so it wasn't exported.", filepath.Base(dst), what).
			WithHint("Try again; if it happens again, this machine's copy may be damaged.").
			WithDetails(map[string]any{"path": dst, "sha256": got, "expected": wantSHA}).WithCommand("metarush cache status")
	}
	return placeNoClobber(tmp, dst)
}

// placeNoClobber moves tmp to dst only if dst does not exist (hard link when the
// filesystem supports it, so a racing writer can never be overwritten).
func placeNoClobber(tmp, dst string) error {
	defer os.Remove(tmp)
	appeared := func() error {
		return apperr.New(apperr.Conflict, "A file named %s appeared in that folder while exporting, and MetaRush never overwrites files.", filepath.Base(dst)).
			WithHint("Choose another folder, or move it and export again.").WithDetails(map[string]any{"path": dst})
	}
	if err := os.Link(tmp, dst); err == nil {
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return appeared()
	}
	if _, err := os.Lstat(dst); err == nil {
		return appeared()
	}
	return os.Rename(tmp, dst)
}

func writeExclusive(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return apperr.Wrap(apperr.Conflict, err, "That folder already has %s, and MetaRush never overwrites files.", filepath.Base(path)).
			WithHint("Choose another folder, or move it first.").WithDetails(map[string]any{"path": path})
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// DeliveryReceipt is the receipt's part in a delivered video's record (D240).
type DeliveryReceipt struct {
	Format string `json:"format" jsonschema:"description=metarush.receipt/1"`
	// CostUSD is what the version uses of outside AI services, at cost; Cost its parts.
	CostUSD float64         `json:"costUsd"`
	Cost    *costs.Estimate `json:"cost,omitempty"`
	// ProjectUSD is everything the project spent on them.
	ProjectUSD float64  `json:"projectUsd"`
	Requests   int      `json:"requests" jsonschema:"description=Requests the project made to outside AI services."`
	Services   []string `json:"services"`
	Deleted    int      `json:"deleted" jsonschema:"description=Of them\\, deleted at the service after the download."`
	// Claude: conversations in the panel about the project, whose account
	// answered and under which terms (D239).
	Claude        int      `json:"claude"`
	ClaudeAnswers []string `json:"claudeAnswers,omitempty"`
	Generated     int      `json:"generated" jsonschema:"description=Generated shots and AI narration lines in the project."`
	AutoUSD       float64  `json:"autoUsd" jsonschema:"description=What Auto spent by itself in the project (what it was charged)."`
	AutoPending   bool     `json:"autoPending,omitempty" jsonschema:"description=Some of what Auto started is still being made: its cost isn't known yet."`
	Lines         []string `json:"lines"`
	Command       string   `json:"command" jsonschema:"description=The command for the whole receipt."`
}

// DeliveryReceiptFor makes a version's receipt for its delivery record; the
// receipt package sets it (it reads other packages' records).
var DeliveryReceiptFor func(ctx context.Context, a *app.App, p *models.Project, r *models.Revision) *DeliveryReceipt

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("deliver.export", Offering{ID: "delivery", Group: "deliver", Title: "Delivery",
		Does:  "Exports finished videos to a folder with a notes file each, checked, and never over an older file.",
		Words: []string{"hand off", "send the final video", "export"}})
}
