// Package keep remembers what conversations offered to keep (D166,
// docs/keeping.md): one record per project and content, so a "Keep this?"
// card is never shown twice for the same thing, and what was kept or
// declined stays decided.
package keep

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Norm is text compared: lower case, letters and digits only.
func Norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LinkKey is an address compared: no scheme, no "www.", no trailing slash or
// fragment, the host in lower case (the same address typed two ways is one).
func LinkKey(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	p := strings.TrimRight(u.EscapedPath(), "/")
	k := host + p
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// Key is what makes two offers the same thing (per project).
func Key(kind, text string, d models.KeepDetail) string {
	switch kind {
	case models.KeepSource:
		if d.URL != "" {
			return kind + ":" + LinkKey(d.URL)
		}
		return kind + ":" + LinkKey(text)
	case models.KeepPicture:
		switch {
		case d.ResourceID != "":
			return kind + ":" + d.ResourceID
		case d.Attachment != "":
			return kind + ":" + d.Attachment
		case d.URL != "":
			return kind + ":" + LinkKey(d.URL)
		}
	case models.KeepBrief:
		var parts []string
		for _, x := range []string{d.Goal, d.Audience, d.Tone} {
			parts = append(parts, Norm(x))
		}
		for _, dl := range d.Deliverables {
			parts = append(parts, Norm(dl.Name))
		}
		return kind + ":" + strings.Join(parts, "|")
	case models.KeepRule, models.KeepCheck:
		// A rule and a check with the same words are the same thing.
		return "rule:" + Norm(text)
	case models.KeepPerson:
		return kind + ":" + Norm(d.Person) + ":" + Norm(text)
	}
	return kind + ":" + Norm(text)
}

// ErrSeen means the thing was offered before (asked, kept or declined).
var ErrSeen = errors.New("keep: already offered")

// Ask records an offer, unless the same thing was offered in that project
// before: then it returns the earlier record and ErrSeen. The unique index
// makes two machines or two turns ask only once.
func Ask(ctx context.Context, db *store.DB, workspaceID string, it models.KeepItem) (*models.KeepItem, error) {
	if it.Key == "" {
		it.Key = Key(it.Kind, it.Text, it.Detail)
	}
	if prev, err := Find(ctx, db, workspaceID, it.ProjectID, it.Key); err == nil {
		return prev, ErrSeen
	}
	it.ID = ids.New(ids.Keep)
	it.WorkspaceID = workspaceID
	if it.State == "" {
		it.State = models.KeepAsked
	}
	if it.AskedAt.IsZero() {
		it.AskedAt = time.Now().UTC()
	}
	if _, err := db.C(store.CKeep).InsertOne(ctx, it); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			if prev, ferr := Find(ctx, db, workspaceID, it.ProjectID, it.Key); ferr == nil {
				return prev, ErrSeen
			}
		}
		return nil, err
	}
	return &it, nil
}

// Find reads the record of a thing (store.ErrNotFound when it was never offered).
func Find(ctx context.Context, db *store.DB, workspaceID, projectID, key string) (*models.KeepItem, error) {
	var it models.KeepItem
	err := db.C(store.CKeep).FindOne(ctx, bson.M{"workspaceId": workspaceID, "projectId": projectID, "key": key}).Decode(&it)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// Get reads a record by its ID.
func Get(ctx context.Context, db *store.DB, workspaceID, id string) (*models.KeepItem, error) {
	var it models.KeepItem
	if err := db.GetByID(ctx, store.CKeep, workspaceID, id, &it); err != nil {
		return nil, err
	}
	return &it, nil
}

// Decide marks a thing kept or declined (creating its record when it was
// never asked: a person kept it at a terminal).
func Decide(ctx context.Context, db *store.DB, workspaceID string, it models.KeepItem, state string, by models.Actor, outcome string) (*models.KeepItem, error) {
	if it.Key == "" {
		it.Key = Key(it.Kind, it.Text, it.Detail)
	}
	now := time.Now().UTC()
	set := bson.M{"state": state, "decidedAt": now, "decidedBy": by}
	if outcome != "" {
		set["outcome"] = outcome
	}
	onInsert := bson.M{"_id": ids.New(ids.Keep), "kind": it.Kind, "text": it.Text, "detail": it.Detail, "from": orDefault(it.From, "person"), "askedAt": now}
	if it.ConversationID != "" {
		onInsert["conversationId"] = it.ConversationID
	}
	var out models.KeepItem
	err := db.C(store.CKeep).FindOneAndUpdate(ctx, bson.M{"workspaceId": workspaceID, "projectId": it.ProjectID, "key": it.Key},
		bson.M{"$set": set, "$setOnInsert": onInsert}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// SetAction links a record to the card that asked about it.
func SetAction(ctx context.Context, db *store.DB, workspaceID, id, actionID string) {
	_, _ = db.C(store.CKeep).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": workspaceID}, bson.M{"$set": bson.M{"actionId": actionID}})
}

// List reads a project's records, newest first (state "" = every state).
func List(ctx context.Context, db *store.DB, workspaceID, projectID, state string) ([]models.KeepItem, error) {
	f := bson.M{"projectId": projectID}
	if state != "" {
		f["state"] = state
	}
	var out []models.KeepItem
	if err := db.Find(ctx, store.CKeep, workspaceID, f, &out, options.Find().SetSort(bson.D{{Key: "askedAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	if out == nil {
		out = []models.KeepItem{}
	}
	return out, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
