package remotion

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevertex"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	_ "github.com/metavert-io/metarush/internal/videogen" // the Vertex AI integration, as the binary has it
)

// A service account's private key never goes into a refresh's steps (D282):
// the plan refuses it, and so does the run, whatever its record says.
func TestRefreshNeverGetsAPrivateKey(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	_, key := fakevertex.Start(t)
	kept, err := providers.NormalizeVertexKey(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvVar("vertex"), kept)
	setKey(t, a, fakeKey())
	s := newSource(t, a, "privatefilm")
	v := versionOf(t, a, s, refreshProject(""), "", models.RemotionKindImport)
	in := RemotionRefreshIn{Source: s.Name, Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "vertex", Env: "GOOGLE_KEY"}}, Outputs: []string{"public/"}}
	p, err := planRefresh(ctx, a, s, v, &in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready || len(p.Keys) != 0 || !strings.Contains(strings.Join(p.Problems, " | "), "Vertex AI key is a private key that never leaves MetaRush") {
		t.Fatalf("plan: %+v %q", p.Keys, p.Problems)
	}

	needsSandbox(t, a)
	r := startTestRefresh(t, a, s, RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}}, Outputs: []string{"public/"}})
	if err := a.DB.Update(ctx, store.CRemotionRefreshes, a.WorkspaceID(), r.ID, map[string]any{"$set": map[string]any{"refreshSteps.keys.0.provider": "vertex"}}); err != nil {
		t.Fatal(err)
	}
	var checked []string
	if _, err := testRefresher(a, &checked).run(ctx, r.ID, r.JobID, nil); err == nil {
		t.Fatal("ran with a private key")
	}
	done, _ := getRefresh(ctx, a, r.ID)
	if done.State != models.RemotionRunFailed || !strings.Contains(done.Error, "private key") || len(done.Steps) != 0 || done.Ran {
		t.Fatalf("record: %+v", done)
	}
}
