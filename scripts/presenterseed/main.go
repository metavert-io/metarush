// Command presenterseed puts a person with recorded consent covering a
// presenter at D-ID, an approved look and a presenter waiting for its test
// clip into a throwaway test database, for scripts/presenters-e2e.sh: the
// steps that need a person on camera (recording consent, the training take)
// can't be played by a browser run. It refuses anything but a local
// metarush-test-* database. A development helper (go run
// ./scripts/presenterseed), not part of the metarush binary.
//
//	go run ./scripts/presenterseed <mongodb-uri> <metarush-test-…> <consent clip> <presenter's ID at the fake D-ID>
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/presenterseed <mongodb-uri> <metarush-test-name> <consent clip> <presenter id at the fake>")
		os.Exit(2)
	}
	uri, name, clip, avatar := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if !strings.HasPrefix(name, store.TestDBPrefix) || !(strings.Contains(uri, "127.0.0.1") || strings.Contains(uri, "localhost")) {
		fmt.Fprintf(os.Stderr, "refusing %q at %q: only local %s* databases are seeded\n", name, uri, store.TestDBPrefix)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		fail(err)
	}
	defer db.Close(ctx)
	var ws models.Workspace
	if err := db.C(store.CWorkspaces).FindOne(ctx, bson.M{}).Decode(&ws); err != nil {
		fail(fmt.Errorf("no workspace: %w", err))
	}
	var as models.Asset
	if err := db.C(store.CAssets).FindOne(ctx, bson.M{"_id": clip}).Decode(&as); err != nil {
		fail(fmt.Errorf("no clip %s: %w", clip, err))
	}
	now := time.Now().UTC()
	actor := models.Actor{UserID: "usr_e2e", UserName: "E2E", Via: "cli"}
	person := models.Person{ID: ids.New(ids.Person), WorkspaceID: ws.ID, Name: "Ada Lovelace", NameKey: speakers.NameKey("Ada Lovelace"), CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
	exp := now.AddDate(1, 0, 0)
	consent := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: ws.ID, PersonID: person.ID, PersonName: person.Name, Self: true, RecordingID: as.ID,
		Statement: "I, Ada Lovelace, agree to a presenter of me kept at D-ID, for my own videos.", Scope: models.ConsentScope{AllProjects: true},
		Uses: []string{models.ConsentShots, models.ConsentVoice, models.ConsentPresenters}, Providers: []string{"d-id"}, ExpiresAt: &exp, Adult: true,
		RecordedBy: actor, RecordedAt: now, Machine: "e2e"}
	look := models.GenLook{ID: ids.New(ids.Look), WorkspaceID: ws.ID, Name: "ada", Title: "Ada Lovelace", Subject: models.LookPerson, PersonID: person.ID,
		PersonName: person.Name, Style: models.LookRealistic, Description: "A woman with dark hair, in a green jacket.", Stills: []models.LookStill{}, Clips: []models.LookClip{}, Version: "e2e0look0001", State: models.LookApproved,
		ConsentID: consent.ID, ApprovedBy: &actor, ApprovedAt: &now, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	pr := models.Presenter{ID: ids.New(ids.Presenter), WorkspaceID: ws.ID, Name: "ada", Title: "Ada Lovelace", PersonID: person.ID, PersonName: person.Name,
		LookID: look.ID, LookName: look.Name, LookVersion: look.Version, ConsentID: consent.ID, Provider: "d-id", ProviderTitle: "D-ID", Model: "express",
		GreenScreen: true, ProviderID: avatar, State: models.PresenterDraft, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	for _, x := range []struct {
		c string
		v any
	}{{store.CPeople, person}, {store.CConsents, consent}, {store.CLooks, look}, {store.CPresenters, pr}} {
		if err := db.Insert(ctx, x.c, x.v); err != nil {
			fail(err)
		}
	}
	fmt.Printf("seeded: Ada Lovelace, her consent (a presenter at D-ID), her look and the presenter “ada” (%s, a draft)\n", pr.ID)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "presenterseed:", err)
	os.Exit(1)
}
