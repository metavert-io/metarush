package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Words changed in a speaker's own voice in the panel (D308–D312): Claude
// prices and reads them; asking for new words and putting them in the cut
// are cards only a person confirms, never run by Auto; taking them out is a
// card too.
func TestSpokenWordRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, _ := fixture(t, a)
	ctx := context.Background()
	m := &Manager{App: a, names: map[string]string{}}
	parse := func(command string) cliCall {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %s", command)
		}
		return call
	}
	for _, read := range []string{"metarush cut say estimate " + projectID + " --clip " + assetID + " --from 1 --to 1.5 --as fifteen --json",
		"metarush cut say list " + projectID, "metarush cut say show swc_01jabcdefghjkmnpqrstvwxyz0"} {
		if call := parse(read); !readOnlyCall(call) {
			t.Errorf("%s is a read Claude may run (%+v)", read, call)
		}
	}
	card := func(command string) *AssistantAction {
		t.Helper()
		call := parse(command)
		if readOnlyCall(call) {
			t.Fatalf("%s should be refused for Claude", command)
		}
		act := &AssistantAction{Kind: call.Path}
		m.describeAction(ctx, nil, &call, act)
		for _, s := range append([]string{act.Title}, act.Details...) {
			if err := plainwords.CheckPanel(s); err != nil {
				t.Errorf("%s: card text %q: %v", command, s, err)
			}
			if strings.Contains(s, "swc_") || strings.Contains(s, "ast_") || strings.Contains(s, "prj_") || strings.Contains(s, "--") {
				t.Errorf("card text %q shows an ID or a flag", s)
			}
		}
		return act
	}
	// Making new words: nobody named the voice that says them, so it can't be confirmed.
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: assetID, From: 1, To: 2.5,
		Text: "we planted twelve rows", Speaker: "speaker_00", Words: []models.Word{{Text: "we", From: 1, To: 1.2}, {Text: "planted", From: 1.25, To: 1.6},
			{Text: "twelve", From: 1.65, To: 2}, {Text: "rows", From: 2.05, To: 2.4}}}); err != nil {
		t.Fatal(err)
	}
	mk := card("metarush cut say make " + projectID + " --clip " + assetID + " --from 1.65 --to 2 --as fifteen")
	if !mk.HumanOnly || mk.State != ActionUnavailable || mk.Unavailable == "" {
		t.Fatalf("a request that can't be made is a card saying why: %+v", mk)
	}
	if none := card("metarush cut say make " + projectID + " --clip " + assetID + " --from 1.65 --to 2"); none.Op != "" {
		t.Fatalf("no new words: %+v", none)
	}
	// Putting a ready change in, and taking it out.
	now := time.Now().UTC()
	c := models.SpokenChange{ID: ids.New(ids.SpokenChange), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, Branch: "main", AssetID: assetID, From: 1, To: 1.5,
		Before: "twelve", Words: "fifteen", Speaker: models.SpokenSpeaker{PersonID: "per_x", Name: "Ada"}, State: models.SpokenReady, Sound: assetID,
		Check: &models.SpokenCheck{Heard: "fifteen", Passed: true, WordsMatch: true, VoiceMatch: true}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CSpokenChanges, c); err != nil {
		t.Fatal(err)
	}
	place := card("metarush cut say place " + c.ID)
	var in ops.CutSayPlaceIn
	_ = json.Unmarshal([]byte(place.Input), &in)
	if place.Op != "cut.say.place" || !place.HumanOnly || in.ID != c.ID || !strings.Contains(place.Title, "“fifteen”") || !savesVersion(place.Op) {
		t.Fatalf("place card: %+v", place)
	}
	undo := card("metarush cut say undo " + c.ID)
	if undo.Op != "cut.say.undo" || undo.HumanOnly || !strings.Contains(undo.Title, "“twelve”") || !savesVersion(undo.Op) {
		t.Fatalf("undo card: %+v", undo)
	}
	// Never by itself: not Auto's, not priced Auto's.
	conv := &conv{}
	for _, act := range []*AssistantAction{place, undo, mk} {
		x := *act
		x.State = ActionPending
		if autoOps[x.Op] || pricedOps[x.Op] || conv.autoStep(&x) || conv.pricedStep(&x) {
			t.Fatalf("%s may run by itself", x.Op)
		}
	}
}
