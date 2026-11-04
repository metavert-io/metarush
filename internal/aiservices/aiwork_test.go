package aiservices

import (
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

// Every kind of AI work has its answer to "what did AI do to it" (D435), the
// one Content Credentials, the delivery's record and the quality check read:
// a new kind fails here until it is decided.
func TestEveryKindSaysWhatAIDid(t *testing.T) {
	want := map[string]string{
		KindVideo: models.AIWorkMade, KindPicture: models.AIWorkMade, KindVoice: models.AIWorkMade, KindSoundEffect: models.AIWorkMade,
		KindMusic: models.AIWorkMade, KindDubbing: models.AIWorkMade, KindPresenter: models.AIWorkMade,
		KindVoiceChange: models.AIWorkEdited, KindLipSync: models.AIWorkEdited,
		KindBackground: models.AIWorkEnhanced, KindUpscale: models.AIWorkEnhanced,
		KindVoiceCopy: "", KindPresenterMake: "", KindTranscription: "",
	}
	for _, k := range Kinds() {
		w, ok := want[k.Kind]
		if !ok {
			t.Errorf("the kind %q has no answer to what AI did to what it makes: add it to models.AIWorkOf and here", k.Kind)
			continue
		}
		if got := models.AIWorkOf(k.Kind, k.Edit); got != w {
			t.Errorf("%s: %q, want %q", k.Kind, got, w)
		}
	}
	if models.AIWorkOf("", false) != models.AIWorkMade || models.AIWorkOf("picture", true) != models.AIWorkEnhanced || models.AIWorkOf("a kind from later", false) != models.AIWorkMade {
		t.Error("generated video, older edited pictures or a kind not known yet")
	}
}
