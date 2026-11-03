package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

func h64(c string) string { return strings.Repeat(c, 64) }

// A delivered video's manifest says what each kind of AI work made or
// changed, with the IPTC source type C2PA uses (D419), consistent with the
// record's disclosure; it names no project, clip, person, voice or prompt.
func TestManifestSaysWhatAIDidAndNoNames(t *testing.T) {
	it := &deliveryItem{
		proj: models.Project{Name: "Acme launch teaser"},
		rev: models.Revision{Spec: editspec.Spec{
			Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
				{ID: "b1", Take: &editspec.TakeRef{TakeID: "vtk_1", AssetID: "ast_tts", AIVoice: true}},
				{ID: "b2", Take: &editspec.TakeRef{TakeID: "vtk_2", AssetID: "ast_read", Processed: &editspec.ProcessedRef{AssetID: "ast_readclean"}, UseProcessed: true}},
			}},
			Mix: &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: "ast_footage", Variant: "ast_clean"}}},
		}},
		assets: map[string]models.Asset{
			"ast_footage":   {ID: "ast_footage", Kind: "video", Name: "ada-lovelace-interview.mov", SHA256: h64("1"), MIME: "video/quicktime"},
			"ast_gen":       {ID: "ast_gen", Kind: "video", SHA256: h64("2"), MIME: "video/mp4"},
			"ast_tts":       {ID: "ast_tts", Kind: "audio", SHA256: h64("3"), MIME: "audio/mpeg"},
			"ast_clean":     {ID: "ast_clean", Kind: "audio", SHA256: h64("4"), MIME: "audio/wav"},
			"ast_lips":      {ID: "ast_lips", Kind: "video", SHA256: h64("5"), MIME: "video/mp4"},
			"ast_said":      {ID: "ast_said", Kind: "audio", SHA256: h64("6"), MIME: "audio/wav"},
			"ast_music":     {ID: "ast_music", Kind: "audio", SHA256: h64("7"), MIME: "audio/mpeg"},
			"ast_read":      {ID: "ast_read", Kind: "audio", SHA256: h64("8"), MIME: "audio/wav"},
			"ast_readclean": {ID: "ast_readclean", Kind: "audio", SHA256: h64("9"), MIME: "audio/wav"},
			"ast_presenter": {ID: "ast_presenter", Kind: "video", SHA256: h64("a"), MIME: "video/mp4"},
		},
		takes: map[string]models.VoiceTake{"vtk_1": {ID: "vtk_1", Provider: "elevenlabs", VoiceName: "Ada Lovelace (voice copy)", AIVoice: true}},
	}
	sc := &DeliverySidecar{
		Sources: []DeliverySource{
			{AssetID: "ast_footage", Name: "ada-lovelace-interview.mov", Kind: "video", SHA256: h64("1"), UsedAs: []string{"clip:c1"}},
			{AssetID: "ast_gen", Kind: "video", SHA256: h64("2"), UsedAs: []string{"clip:c2"}},
			{AssetID: "ast_music", Kind: "audio", SHA256: h64("7"), UsedAs: []string{"audio:t1(music)"}},
		},
		Generated: &DeliveryGenerated{Clips: []DeliveryGeneratedClip{
			{AssetID: "ast_gen", Title: "Acme rocket at dawn", Provider: "Google Gemini", Model: "veo-3.1", SHA256: h64("2"), UsedAs: []string{"clip:c2"}},
			{AssetID: "ast_lips", Kind: "lip-sync", Title: "Ada's mouth", Provider: "sync.", Model: "lipsync-2", SHA256: h64("5")},
			{AssetID: "ast_said", Kind: "voice", Title: "Ada says Tuesday", Provider: "ElevenLabs", Model: "eleven_v3", SHA256: h64("6"), UsedAs: []string{"said:swc_1"}},
			{AssetID: "ast_presenter", Kind: "presenter", Title: "Ada presents", Presenter: "Ada Lovelace", Consent: "cst_1", Provider: "D-ID", Model: "v4", SHA256: h64("a")},
			{Kind: "picture", ResourceID: "res_1", Title: "Kite", Provider: "Google Gemini", Model: "nano-banana", SHA256: h64("b")},
			{Kind: "picture", ResourceID: "res_2", Title: "Ada cut out", Provider: "this computer", Model: "background-removal", SHA256: h64("c"), Edited: true},
		}},
		Resources: []DeliveryResource{{Name: "logo", ResourceID: "res_3", Title: "Acme logo", Kind: "image", SHA256: h64("d")}, {Name: "font", Kind: "font", SHA256: h64("e")}},
	}
	m := manifestFor(t.Context(), nil, it, sc)
	def, err := m.Definition()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Title       string `json:"title"`
		Ingredients []struct {
			Title       string `json:"title"`
			InstanceID  string `json:"instance_id"`
			Source      string `json:"digital_source_type"`
			Description string `json:"description"`
		} `json:"ingredients"`
		Assertions []struct {
			Label   string `json:"label"`
			Created bool   `json:"created"`
			Data    struct {
				All     bool `json:"allActionsIncluded"`
				Actions []struct {
					Action string `json:"action"`
					Source string `json:"digitalSourceType"`
				} `json:"actions"`
			} `json:"data"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(def, &d); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ // the ingredient's title → its source type
		"AI-generated video 1":       c2pa.SourceTrained,
		"Footage edited with AI 1":   c2pa.SourceAIEdited,
		"Words said with AI 1":       c2pa.SourceTrained,
		"AI presenter 1":             c2pa.SourceTrained,
		"AI-generated picture 1":     c2pa.SourceTrained,
		"Picture changed with AI 1":  c2pa.SourceEnhanced,
		"AI voice 1":                 c2pa.SourceTrained,
		"Sound cleaned up with AI 1": c2pa.SourceEnhanced,
		"Sound cleaned up with AI 2": c2pa.SourceEnhanced,
		"Sound 1":                    "",
		"Footage 1":                  "",
		"Music 1":                    "",
		"Picture 1":                  "",
	}
	got := map[string]string{}
	for _, ing := range d.Ingredients {
		got[ing.Title] = ing.Source
		if !strings.HasPrefix(ing.InstanceID, "sha256:") || len(ing.InstanceID) != 7+64 {
			t.Errorf("%s: %q", ing.Title, ing.InstanceID)
		}
	}
	for title, src := range want {
		if g, ok := got[title]; !ok || g != src {
			t.Errorf("%s: %q (listed %v), want %q", title, g, ok, src)
		}
	}
	if len(d.Ingredients) != len(want) {
		t.Errorf("%d ingredients, want %d: %v", len(d.Ingredients), len(want), got)
	}
	acts := d.Assertions[0].Data.Actions
	if d.Title != c2pa.Title || d.Assertions[0].Label != "c2pa.actions" || !d.Assertions[0].Created || !d.Assertions[0].Data.All ||
		acts[0].Action != "c2pa.created" || acts[0].Source != c2pa.SourceSynthetic || acts[len(acts)-1].Action != "c2pa.edited" || len(acts) != len(want)+2 {
		t.Fatalf("actions: %+v", d.Assertions)
	}
	low := strings.ToLower(string(def))
	for _, never := range []string{"acme", "ada", "lovelace", "rocket", "kite", "tuesday", "interview", "cst_", "swc_", "res_", "ast_", "logo"} {
		if strings.Contains(low, never) {
			t.Errorf("the manifest says %q", never)
		}
	}
	if !strings.Contains(string(def), "Made with Google Gemini, veo-3.1") || !strings.Contains(string(def), "Changed with this computer, background-removal") {
		t.Errorf("what made the AI parts isn't said: %s", def)
	}
	// Without AI: a composite, no AI source types.
	plain := manifestFor(t.Context(), nil, &deliveryItem{assets: it.assets}, &DeliverySidecar{Sources: sc.Sources[:1]})
	if plain.SourceType() != c2pa.SourceComposite {
		t.Fatalf("no AI: %s", plain.SourceType())
	}
}
