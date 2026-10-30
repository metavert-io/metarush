package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Whether a consent behind changed words, or a matched mouth, has ended goes
// by the database server's clock (D360): every computer holds a version back
// from the same moment. A computer ten minutes fast still lets through words
// whose consent ends in five minutes; one ten minutes slow holds back words
// whose consent ended five minutes ago.
func TestChangedWordsConsentEndsByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	consent := func(ends time.Time) models.Consent {
		c := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: a.WorkspaceID(), PersonID: "per_ines", PersonName: "Ines Calloway", Self: true,
			Statement: "I agree.", Scope: models.ConsentScope{AllProjects: true}, Uses: []string{models.ConsentVoice, models.ConsentUseNewWords, models.ConsentUseLipSync},
			Providers: []string{"elevenlabs", "sync"}, ExpiresAt: &ends, Adult: true, RecordedBy: a.Actor, RecordedAt: server.Add(-time.Hour)}
		if err := a.DB.Insert(ctx, store.CConsents, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	voice := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID()}
	if err := a.DB.Insert(ctx, store.CGenerations, voice); err != nil {
		t.Fatal(err)
	}
	change := func(c models.Consent) *models.SpokenChange {
		mouth := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), ConsentIDs: []string{c.ID}}
		if err := a.DB.Insert(ctx, store.CGenerations, mouth); err != nil {
			t.Fatal(err)
		}
		return &models.SpokenChange{ID: ids.New(ids.SpokenChange), WorkspaceID: a.WorkspaceID(), ProjectID: ids.New(ids.Project), Speaker: models.SpokenSpeaker{Name: "Ines Calloway"},
			VoiceGeneration: voice.ID, ConsentID: c.ID, Provider: "elevenlabs", State: models.SpokenPlaced,
			Mouth: &models.SpokenMouth{GenerationID: mouth.ID, State: "ready"}}
	}
	inForce := change(consent(server.Add(5 * time.Minute)))
	ended := change(consent(server.Add(-5 * time.Minute)))

	restore := testutil.SkewClock(t, 10*time.Minute)
	if why, err := saidWithdrawn(ctx, a, inForce); err != nil || why != "" {
		t.Fatalf("a computer ten minutes fast held back words whose consent ends in five minutes: %q %v", why, err)
	}
	if why, err := mouthWithdrawn(ctx, a, inForce, inForce.ProjectID); err != nil || why != "" {
		t.Fatalf("a computer ten minutes fast held back a mouth whose consent ends in five minutes: %q %v", why, err)
	}
	restore()
	restore = testutil.SkewClock(t, -10*time.Minute)
	if why, err := saidWithdrawn(ctx, a, ended); err != nil || !strings.Contains(why, "no longer in force") {
		t.Fatalf("a computer ten minutes slow let through words whose consent ended five minutes ago: %q %v", why, err)
	}
	if why, err := mouthWithdrawn(ctx, a, ended, ended.ProjectID); err != nil || !strings.Contains(why, "no longer in force") {
		t.Fatalf("a computer ten minutes slow let through a mouth whose consent ended five minutes ago: %q %v", why, err)
	}
	if why, err := notCoveredHere(ctx, a, []string{ended.ConsentID}, ids.New(ids.Project), ""); err != nil || !strings.Contains(why, "doesn't cover") {
		t.Fatalf("a computer ten minutes slow found an ended consent covering another project: %q %v", why, err)
	}
	restore()
}
