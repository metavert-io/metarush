package ops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

func h64(c string) string { return strings.Repeat(c, 64) }

// hx is a distinct SHA-256-shaped hash for test parts.
func hx(n int) string { return fmt.Sprintf("%064x", n) }

// The cut the test version nests.
var (
	introProject = "prj_" + strings.Repeat("i", 26)
	introVersion = "rev_" + strings.Repeat("i", 26)
)

// aiVersion is a delivered version with every kind of AI work MetaRush
// records, in the cut itself, in a cut it nests and in a conversation it
// plays, with names that must never reach a manifest.
func aiVersion() *deliveryItem {
	gen := func(id, kind, provider, model, sha string) models.Asset {
		return models.Asset{ID: id, Kind: "video", SHA256: sha, MIME: "video/mp4", Name: id + ".mp4",
			Source: &models.AssetSource{Platform: models.PlatformGenerated, Generated: &models.GeneratedSource{Kind: kind, ProviderTitle: provider, Model: model, GenerationID: "gen_" + id}}}
	}
	sound := func(id, sha string) models.Asset {
		return models.Asset{ID: id, Kind: "audio", SHA256: sha, MIME: "audio/wav"}
	}
	said := gen("ast_said", "voice", "ElevenLabs", "eleven_v3", hx(6))
	said.Kind, said.MIME, said.Source.Generated.Said = "audio", "audio/wav", &models.GeneratedSaid{}
	sfx := gen("ast_sfx", "sound-effect", "ElevenLabs", "sfx_v2", hx(15))
	sfx.Kind, sfx.MIME = "audio", "audio/mpeg"
	outer := map[string]models.Asset{
		"ast_footage":   {ID: "ast_footage", Kind: "video", Name: "ada-lovelace-interview.mov", SHA256: hx(1), MIME: "video/quicktime"},
		"ast_gen":       gen("ast_gen", "", "Google Gemini", "veo-3.1", hx(2)),
		"ast_lips":      gen("ast_lips", "lip-sync", "sync.", "lipsync-2", hx(5)),
		"ast_presenter": gen("ast_presenter", "presenter", "D-ID", "v4", hx(10)),
		"ast_said":      said,
		"ast_music":     {ID: "ast_music", Kind: "audio", SHA256: hx(7), MIME: "audio/mpeg"},
		"ast_tts":       sound("ast_tts", hx(3)),
		"ast_read":      sound("ast_read", hx(8)),
		"ast_readclean": sound("ast_readclean", hx(9)),
		"ast_m1":        {ID: "ast_m1", Kind: "video", Name: "acme-call-ada.mp4", SHA256: hx(11), MIME: "video/mp4"},
		"ast_m2":        {ID: "ast_m2", Kind: "video", Name: "acme-call-bob.mp4", SHA256: hx(12), MIME: "video/mp4"},
		"ast_m1clean":   sound("ast_m1clean", hx(13)),
	}
	nestedAssets := map[string]*models.Asset{
		"ast_intro": {ID: "ast_intro", Kind: "video", Name: "acme-rocket-intro.mov", SHA256: hx(14), MIME: "video/quicktime"},
		"ast_ntts":  {ID: "ast_ntts", Kind: "audio", SHA256: hx(16), MIME: "audio/mpeg"},
		"ast_sfx":   &sfx,
	}
	pic := func(id, title, sha string, mw *models.ResourceMadeWith) *models.Resource {
		return &models.Resource{ID: id, Name: id, Title: title, Kind: "image", Versions: []models.ResourceVersion{{SHA256: sha, MIME: "image/png", Source: models.ResourceSource{Kind: "generated", MadeWith: mw}}}}
	}
	outerPics := map[string]*models.Resource{
		"res_kite": pic("res_kite", "Kite", hx(4), &models.ResourceMadeWith{Kind: "ai", AIKind: "picture", ProviderTitle: "Google Gemini", Model: "nano-banana"}),
		"res_cut":  pic("res_cut", "Ada cut out", hx(17), &models.ResourceMadeWith{Kind: "ai", Edit: true, ProviderTitle: "this computer", Model: "background-removal"}),
		"res_big":  pic("res_big", "Ada larger", hx(18), &models.ResourceMadeWith{Kind: "ai", AIKind: "upscale", ProviderTitle: "this computer", Model: "real-esrgan"}),
		"res_logo": {ID: "res_logo", Name: "logo", Title: "Acme logo", Kind: "image", Versions: []models.ResourceVersion{{SHA256: hx(19), MIME: "image/svg+xml", Source: models.ResourceSource{Kind: "file"}}}},
	}
	nestedPics := map[string]*models.Resource{
		"res_nart": pic("res_nart", "Rocket art", hx(20), &models.ResourceMadeWith{Kind: "ai", AIKind: "picture", ProviderTitle: "Google Gemini", Model: "nano-banana"}),
	}
	pinOf := func(r *models.Resource) editspec.ResourcePin {
		return editspec.ResourcePin{ID: r.ID, SHA256: r.Versions[0].SHA256}
	}
	intro := editspec.Spec{
		Clips:     []editspec.Clip{{ID: "i1", AssetID: "ast_intro", In: 0, Out: 3}},
		Audio:     []editspec.AudioTrack{{ID: "fx", AssetID: "ast_sfx", Role: "effect"}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "n1", Take: &editspec.TakeRef{TakeID: "vtk_n", AssetID: "ast_ntts", AIVoice: true}}}},
		Resources: map[string]editspec.ResourcePin{"art": pinOf(nestedPics["res_nart"])},
	}
	spec := editspec.Spec{
		Clips: []editspec.Clip{
			{ID: "c1", AssetID: "ast_footage", In: 0, Out: 2}, {ID: "c2", AssetID: "ast_gen", In: 0, Out: 2}, {ID: "c3", AssetID: "ast_lips", In: 0, Out: 2},
			{ID: "c4", AssetID: "ast_presenter", In: 0, Out: 2}, {ID: "c5", AssetID: editspec.NestedKey(introProject, introVersion), In: 0, Out: 3},
			{ID: "c6", AssetID: "conv:call", In: 0, Out: 4},
		},
		Conversations: map[string]editspec.ConversationPin{"call": {ID: "cnv_" + strings.Repeat("1", 26), Members: []editspec.ConversationMember{
			{ID: "ada", Name: "Ada Lovelace", AssetID: "ast_m1"}, {ID: "bob", Name: "Bob Acme", AssetID: "ast_m2"}}}},
		Audio: []editspec.AudioTrack{{ID: "t1", AssetID: "ast_music", Role: editspec.RoleMusic}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
			{ID: "b1", Take: &editspec.TakeRef{TakeID: "vtk_1", AssetID: "ast_tts", AIVoice: true}},
			{ID: "b2", Take: &editspec.TakeRef{TakeID: "vtk_2", AssetID: "ast_read", Processed: &editspec.ProcessedRef{AssetID: "ast_readclean"}, UseProcessed: true}},
		}},
		Mix: &editspec.Mix{
			Enhanced: []editspec.EnhancedSound{{AssetID: "ast_m1", Variant: "ast_m1clean"}},
			Said:     []editspec.SaidSound{{ID: "swc_1", Sound: "ast_said", Who: "Ada Lovelace", Words: "Tuesday"}},
		},
		Resources: map[string]editspec.ResourcePin{"kite": pinOf(outerPics["res_kite"]), "cutout": pinOf(outerPics["res_cut"]), "big": pinOf(outerPics["res_big"]), "logo": pinOf(outerPics["res_logo"])},
	}
	ns := &editspec.NestedSource{ProjectID: introProject, RevisionID: introVersion, ProjectName: "Acme rocket intro", Spec: &intro,
		Facts: &dbFacts{assets: nestedAssets, res: &resourceFacts{byID: nestedPics}}}
	return &deliveryItem{
		proj:   models.Project{Name: "Acme launch teaser"},
		rev:    models.Revision{Spec: spec},
		assets: outer,
		takes: map[string]models.VoiceTake{
			"vtk_1": {ID: "vtk_1", Provider: "elevenlabs", ModelID: "eleven_v3", VoiceName: "Ada Lovelace (voice copy)", AIVoice: true},
			"vtk_n": {ID: "vtk_n", Provider: "local", ModelID: "kokoro-v1", VoiceName: "Narrator", AIVoice: true},
		},
		extras: &RenderExtras{res: &resourceFacts{byID: outerPics}, nested: &nestedFacts{byKey: map[string]*editspec.NestedSource{editspec.NestedKey(introProject, introVersion): ns}}},
	}
}

type manifestDoc struct {
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

func readManifest(t *testing.T, m c2pa.Manifest) (manifestDoc, string) {
	t.Helper()
	def, err := m.Definition()
	if err != nil {
		t.Fatal(err)
	}
	var d manifestDoc
	if err := json.Unmarshal(def, &d); err != nil {
		t.Fatal(err)
	}
	return d, string(def)
}

// A delivered video's manifest says what each kind of AI work made or
// changed, with the IPTC source type C2PA uses (D419, D435) — in the cut,
// in the cut it nests (its AI narration, its AI picture, its generated sound
// effect) and in the conversation it plays (a member's voice made clearer);
// an upscaled picture is enhanced, never generated; it names no project,
// clip, person, voice or prompt.
func TestManifestSaysWhatAIDidAndNoNames(t *testing.T) {
	it := aiVersion()
	sc := &DeliverySidecar{Resources: []DeliveryResource{{Name: "logo", ResourceID: "res_logo", Kind: "image", SHA256: hx(19)}, {Name: "font", Kind: "font", SHA256: hx(21)}}}
	m := manifestFor(t.Context(), nil, it, sc)
	d, def := readManifest(t, m)
	want := map[string]string{ // the ingredient's title → its source type
		"AI-generated video 1":       c2pa.SourceTrained,
		"Footage edited with AI 1":   c2pa.SourceAIEdited, // lip sync
		"AI presenter 1":             c2pa.SourceTrained,
		"Words said with AI 1":       c2pa.SourceTrained,
		"AI sound effect 1":          c2pa.SourceTrained,  // in the nested cut
		"AI voice 1":                 c2pa.SourceTrained,  // the cut's narration
		"AI voice 2":                 c2pa.SourceTrained,  // the nested cut's narration (no record of its own)
		"Sound cleaned up with AI 1": c2pa.SourceEnhanced, // a narration line
		"Sound cleaned up with AI 2": c2pa.SourceEnhanced, // a conversation member's voice
		"AI-generated picture 1":     c2pa.SourceTrained,
		"Picture changed with AI 1":  c2pa.SourceEnhanced, // background taken away
		"Picture changed with AI 2":  c2pa.SourceEnhanced, // made larger
		"AI-generated picture 2":     c2pa.SourceTrained,  // the nested cut's picture
		"Footage 1":                  "",
		"Footage 2":                  "", // conversation members
		"Footage 3":                  "",
		"Footage 4":                  "", // the nested cut's footage
		"Music 1":                    "",
		"Sound 1":                    "", // the read narration line
		"Picture 1":                  "", // the logo
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
	low := strings.ToLower(def)
	for _, never := range []string{"acme", "ada", "lovelace", "bob", "rocket", "kite", "tuesday", "interview", "narrator", "cnv_", "swc_", "res_", "ast_", "vtk_", "logo"} {
		if strings.Contains(low, never) {
			t.Errorf("the manifest says %q", never)
		}
	}
	for _, said := range []string{"Made with Google Gemini, veo-3.1", "Changed with this computer, background-removal", "Made with ElevenLabs, eleven_v3", "Made with this computer, kokoro-v1"} {
		if !strings.Contains(def, said) {
			t.Errorf("the manifest doesn't say %q: %s", said, def)
		}
	}
	// Footage and an upscaled picture only: a composite, nothing generated.
	big := aiVersion()
	big.rev.Spec = editspec.Spec{Clips: []editspec.Clip{{ID: "c1", AssetID: "ast_footage", In: 0, Out: 2}}, Resources: map[string]editspec.ResourcePin{"big": it.rev.Spec.Resources["big"]}}
	bm := manifestFor(t.Context(), nil, big, &DeliverySidecar{})
	if bm.SourceType() != c2pa.SourceComposite {
		t.Fatalf("an upscaled picture made the video %s", bm.SourceType())
	}
	if _, generative, changed := bm.Counts(); generative != 0 || changed != 1 {
		t.Fatalf("upscaled: %d generative, %d changed", generative, changed)
	}
	// A cut that only nests an AI voice: the nested narration makes it synthetic.
	voice := aiVersion()
	voice.rev.Spec = editspec.Spec{Clips: []editspec.Clip{{ID: "c5", AssetID: editspec.NestedKey(introProject, introVersion), In: 0, Out: 3}}}
	vm := manifestFor(t.Context(), nil, voice, &DeliverySidecar{})
	if vm.SourceType() != c2pa.SourceSynthetic || !strings.Contains(strings.Join(titles(t, vm), " "), "AI voice 1") {
		t.Fatalf("a nested AI voice: %s %v", vm.SourceType(), titles(t, vm))
	}
}

func titles(t *testing.T, m c2pa.Manifest) []string {
	d, _ := readManifest(t, m)
	var out []string
	for _, ing := range d.Ingredients {
		out = append(out, ing.Title)
	}
	return out
}

// The delivery's record discloses exactly what the manifest says AI made or
// edited, and the quality check counts pictures by the same answer (D435):
// every kind of AI work, in the cut, its nested cut and its conversation.
func TestRecordAndManifestAgreeOnWhatAIDid(t *testing.T) {
	it := aiVersion()
	_, pics, pieces := it.ai(t.Context(), nil)
	var order []string
	used := map[string][]string{}
	for _, c := range it.rev.Spec.Clips {
		if !editspec.IsNested(c.AssetID) && !editspec.IsConversation(c.AssetID) {
			order = append(order, c.AssetID)
			used[c.AssetID] = append(used[c.AssetID], "clip:"+c.ID)
		}
	}
	order = append(order, "ast_said")
	g := aiPictures(nil, it, generatedIn(it, order, used))
	disclosed := []string{}
	for _, c := range g.Clips {
		if c.Work != models.AIWorkMade && c.Work != models.AIWorkEdited {
			t.Errorf("the record discloses %s as %q", c.Title, c.Work)
		}
		disclosed = append(disclosed, c.SHA256)
	}
	// The cut's own narration voice is the record's narration (aiVoice), not a generated clip.
	manifestAI := []string{}
	for _, p := range pieces {
		if models.AIDisclosed(p.Work) && (p.TakeID == "" || p.Nested != "") {
			manifestAI = append(manifestAI, p.SHA256)
		}
	}
	sort.Strings(disclosed)
	sort.Strings(manifestAI)
	if strings.Join(disclosed, ",") != strings.Join(manifestAI, ",") {
		t.Fatalf("the record discloses %d pieces, the manifest says AI made or edited %d:\n%v\n%v", len(disclosed), len(manifestAI), disclosed, manifestAI)
	}
	// Enhanced pictures aren't disclosed as AI-generated, and the record's
	// pictures say what AI did to each.
	for _, p := range pics {
		work := aiWorkOn(nil, it, p.ResourceID, p.SHA256)
		switch p.ResourceID {
		case "res_cut", "res_big":
			if work != models.AIWorkEnhanced || slices.Contains(disclosed, p.SHA256) {
				t.Errorf("%s: %q, disclosed %v", p.ResourceID, work, slices.Contains(disclosed, p.SHA256))
			}
		case "res_kite", "res_nart":
			if work != models.AIWorkMade || !slices.Contains(disclosed, p.SHA256) {
				t.Errorf("%s: %q, disclosed %v", p.ResourceID, work, slices.Contains(disclosed, p.SHA256))
			}
		case "res_logo":
			if work != "" {
				t.Errorf("the logo: %q", work)
			}
		}
	}
	// The quality check's answer for a picture is the same function's.
	for kind, disclosedByQA := range map[string]bool{"picture": true, "background-removal": false, "upscale": false} {
		if models.AIDisclosed(models.AIWorkOf(kind, false)) != disclosedByQA {
			t.Errorf("%s: the quality check would say %v", kind, !disclosedByQA)
		}
	}
}

// A signed copy is placed only as the copy that checked out: a link (or a
// copy) never over a file, then its bytes and its manifest's checked again.
func TestPlacedOnlyAsItWasChecked(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "signed.mp4")
	if err := c2pa.WriteSample(src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "video.mp4")
	s := &c2pa.Signed{Path: src, SHA256: hx(0), ManifestHash: hx(0)}
	if err := placeSigned(s, dst); err == nil || !strings.Contains(err.Error(), "changed after MetaRush checked it") {
		t.Fatalf("a copy that isn't the checked one: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("the unchecked copy was left in place")
	}
	if err := os.WriteFile(dst, []byte("someone's file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := placeSigned(s, dst); err == nil || !strings.Contains(err.Error(), "never overwrites") {
		t.Fatalf("over a file: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "someone's file" {
		t.Fatal("a file was overwritten")
	}
}

// abilityVersion is a delivered version with what abilities brought in
// (D460): a picture the starter comfyui-local made from words (its manifest
// says made), a picture from an ability whose manifest says nothing, a
// stock photo and stock footage an ability found (none), and a sound an
// ability made whose manifest says nothing — next to plain footage.
func abilityVersion() *deliveryItem {
	made := func(name, title, ai string) *models.AbilityMadeWith {
		return &models.AbilityMadeWith{Ability: name + "@1a2b3c4d", Name: name, Title: title, Hash: strings.Repeat("1a2b3c4d", 8), RunID: "abr_" + name, AI: ai}
	}
	pic := func(id, title, sha string, mw *models.AbilityMadeWith) *models.Resource {
		return &models.Resource{ID: id, Name: id, Title: title, Kind: "image", Versions: []models.ResourceVersion{{SHA256: sha, MIME: "image/png",
			Source: models.ResourceSource{Kind: models.FromGenerated, Tool: "ability", MadeWith: &models.ResourceMadeWith{Kind: "ability", Ability: mw}}}}}
	}
	clip := func(id, kind, mime, sha string, mw *models.AbilityMadeWith) models.Asset {
		return models.Asset{ID: id, Kind: kind, SHA256: sha, MIME: mime, Name: id,
			Source: &models.AssetSource{Platform: models.PlatformAbility, Title: id, Tool: "MetaRush ability " + mw.Ability, Method: "ability", MadeWith: mw}}
	}
	pics := map[string]*models.Resource{
		"res_comfy":      pic("res_comfy", "Harbour at dawn", hx(31), made("comfyui-local", "Pictures from ComfyUI on this computer", models.AIWorkMade)),
		"res_undeclared": pic("res_undeclared", "Lighthouse", hx(32), made("picture-maker", "Picture maker", "")),
		"res_stock":      pic("res_stock", "Pier photo", hx(33), made("pexels-stock", "Free stock from Pexels", models.AIWorkNone)),
	}
	assets := map[string]models.Asset{
		"ast_footage": {ID: "ast_footage", Kind: "video", Name: "acme-harbour.mov", SHA256: hx(30), MIME: "video/quicktime"},
		"ast_stock":   clip("ast_stock", "video", "video/mp4", hx(34), made("pexels-stock", "Free stock from Pexels", models.AIWorkNone)),
		"ast_hum":     clip("ast_hum", "audio", "audio/wav", hx(35), made("sound-maker", "Sound maker", "")),
	}
	pinOf := func(r *models.Resource) editspec.ResourcePin {
		return editspec.ResourcePin{ID: r.ID, SHA256: r.Versions[0].SHA256}
	}
	spec := editspec.Spec{
		Clips: []editspec.Clip{{ID: "c1", AssetID: "ast_footage", In: 0, Out: 2}, {ID: "c2", AssetID: "ast_stock", In: 0, Out: 2}},
		Audio: []editspec.AudioTrack{{ID: "hum", AssetID: "ast_hum", Role: "effect"}},
		Resources: map[string]editspec.ResourcePin{"dawn": pinOf(pics["res_comfy"]), "lighthouse": pinOf(pics["res_undeclared"]),
			"pier": pinOf(pics["res_stock"])},
	}
	return &deliveryItem{proj: models.Project{Name: "Acme harbour"}, rev: models.Revision{Spec: spec}, assets: assets,
		extras: &RenderExtras{res: &resourceFacts{byID: pics}}}
}

// The reviewer's case (D460): a picture the starter ability comfyui-local
// made from words (Stable Diffusion in ComfyUI) shown in a cut. Content
// Credentials call it an AI-generated picture made with that ability and the
// video compositeSynthetic — never a plain "Picture" in a composite with
// every action said and no generative AI; an output whose manifest declares
// nothing counts as made; what an ability found and declares none of stays a
// plain part.
func TestAnAbilitysAIPictureIsInContentCredentials(t *testing.T) {
	it := abilityVersion()
	// Only the footage and the ComfyUI picture: the reviewer's cut.
	only := abilityVersion()
	only.rev.Spec = editspec.Spec{Clips: it.rev.Spec.Clips[:1], Resources: map[string]editspec.ResourcePin{"dawn": it.rev.Spec.Resources["dawn"]}}
	om := manifestFor(t.Context(), nil, only, &DeliverySidecar{Resources: []DeliveryResource{{Name: "dawn", ResourceID: "res_comfy", Kind: "image", SHA256: hx(31)}}})
	od, odef := readManifest(t, om)
	acts := od.Assertions[0].Data.Actions
	if om.SourceType() != c2pa.SourceSynthetic || acts[0].Action != "c2pa.created" || acts[0].Source != c2pa.SourceSynthetic || !od.Assertions[0].Data.All {
		t.Fatalf("the reviewer's cut: %s %+v", om.SourceType(), od.Assertions)
	}
	got := map[string]string{}
	for _, ing := range od.Ingredients {
		got[ing.Title] = ing.Source
	}
	if got["AI-generated picture 1"] != c2pa.SourceTrained || got["Footage 1"] != "" || len(got) != 2 {
		t.Fatalf("the reviewer's cut: %v", got)
	}
	if !strings.Contains(odef, "Made with Pictures from ComfyUI on this computer, comfyui-local@1a2b3c4d") {
		t.Fatalf("what made it: %s", odef)
	}
	// Everything: declared made, undeclared (made), declared none.
	sc := &DeliverySidecar{Resources: []DeliveryResource{{Name: "dawn", ResourceID: "res_comfy", Kind: "image", SHA256: hx(31)},
		{Name: "lighthouse", ResourceID: "res_undeclared", Kind: "image", SHA256: hx(32)}, {Name: "pier", ResourceID: "res_stock", Kind: "image", SHA256: hx(33)}}}
	m := manifestFor(t.Context(), nil, it, sc)
	d, def := readManifest(t, m)
	want := map[string]string{
		"AI-generated picture 1": c2pa.SourceTrained, // comfyui-local: made
		"AI-generated picture 2": c2pa.SourceTrained, // its manifest says nothing: made
		"AI-generated sound 1":   c2pa.SourceTrained, // a sound whose manifest says nothing: made
		"Footage 1":              "",
		"Footage 2":              "", // stock footage an ability found (none)
		"Picture 1":              "", // a stock photo an ability found (none)
	}
	got = map[string]string{}
	for _, ing := range d.Ingredients {
		got[ing.Title] = ing.Source
	}
	for title, src := range want {
		if g, ok := got[title]; !ok || g != src {
			t.Errorf("%s: %q (listed %v), want %q", title, g, ok, src)
		}
	}
	if len(d.Ingredients) != len(want) || m.SourceType() != c2pa.SourceSynthetic {
		t.Fatalf("%d ingredients (%v), %s", len(d.Ingredients), got, m.SourceType())
	}
	if _, generative, _ := m.Counts(); generative != 3 {
		t.Fatalf("%d generative parts, want 3", generative)
	}
	for _, never := range []string{"acme", "harbour at dawn", "lighthouse", "pier photo", "res_", "ast_", "abr_"} {
		if strings.Contains(strings.ToLower(def), never) {
			t.Errorf("the manifest says %q", never)
		}
	}
}

// The same case in the delivery's record (D460): the ComfyUI picture, the
// undeclared picture and the undeclared sound are disclosed as AI-generated
// with the ability that made them; the stock photo and footage aren't; the
// record's pictures say what AI did to each; the quality check's answer for
// them is the same.
func TestAnAbilitysAIWorkIsInTheDeliveryRecord(t *testing.T) {
	it := abilityVersion()
	order := []string{"ast_footage", "ast_stock", "ast_hum"}
	used := map[string][]string{"ast_footage": {"clip:c1"}, "ast_stock": {"clip:c2"}, "ast_hum": {"track:hum"}}
	g := aiPictures(nil, it, generatedIn(it, order, used))
	if g == nil {
		t.Fatal("nothing disclosed")
	}
	byTitle := map[string]DeliveryGeneratedClip{}
	for _, c := range g.Clips {
		byTitle[c.Title] = c
	}
	comfy, ok := byTitle["Harbour at dawn"]
	if !ok || comfy.Work != models.AIWorkMade || comfy.Ability != "comfyui-local@1a2b3c4d" || comfy.Provider != "Pictures from ComfyUI on this computer" || comfy.Kind != "picture" {
		t.Fatalf("the ComfyUI picture: %+v", g.Clips)
	}
	if c, ok := byTitle["Lighthouse"]; !ok || c.Work != models.AIWorkMade || c.Ability != "picture-maker@1a2b3c4d" {
		t.Fatalf("the undeclared picture: %+v", g.Clips)
	}
	if c, ok := byTitle["ast_hum"]; !ok || c.Work != models.AIWorkMade || c.Kind != "sound" || c.Ability != "sound-maker@1a2b3c4d" {
		t.Fatalf("the undeclared sound: %+v", g.Clips)
	}
	if _, ok := byTitle["Pier photo"]; ok || len(g.Clips) != 3 {
		t.Fatalf("disclosed what an ability only found: %+v", g.Clips)
	}
	if g.Notice != "This video contains AI-generated material (pictures or sound)." {
		t.Fatalf("the notice: %q", g.Notice)
	}
	for id, want := range map[string]string{"res_comfy": models.AIWorkMade, "res_undeclared": models.AIWorkMade, "res_stock": ""} {
		r := it.extras.res.byID[id]
		if got := aiWorkOn(nil, it, id, r.Versions[0].SHA256); got != want {
			t.Errorf("the record's %s: %q, want %q", id, got, want)
		}
		// The quality check asks the same function (internal/worker/qa.go).
		if disclosed := models.AIDisclosed(models.AIWorkOfMadeWith(r.Versions[0].Source.MadeWith, "")); disclosed != (want != "") {
			t.Errorf("the quality check's %s: disclosed %v", id, disclosed)
		}
	}
	for id, want := range map[string]string{"ast_stock": "", "ast_hum": models.AIWorkMade, "ast_footage": ""} {
		if got := models.AIWorkOfSource(it.assets[id].Source); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}
