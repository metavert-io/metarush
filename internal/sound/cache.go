package sound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Processed audio is cached by what it is made from, so nothing is processed
// — or paid for — twice, on any machine of the workspace:
//
//   - a processed voice ("wet") is keyed by the source's SHA-256, the stretch,
//     the mode and the engine's versions (models, the clean chain, ffmpeg);
//     its WAV is a content-addressed object in the media store;
//   - a copy is keyed by the source, its stretch and channels, and every
//     piece (stretch, mode, strength, the wet it uses) plus the assembly's
//     version; it is an ordinary audio asset (tagged enhanced, kept out of the
//     library lists), so previews and renders fetch it like any file.
//
// Records live in sound_cache with _id <workspace>/<kind>:<key> (no index needed).

// Collection is the cache's MongoDB collection.
const Collection = "sound_cache"

const assemblyVersion = "copy-1"

// TagEnhanced marks a processed copy (kept out of the library's lists).
const TagEnhanced = models.TagEnhanced

// record is a cache entry.
type record struct {
	ID            string        `bson:"_id"`
	WorkspaceID   string        `bson:"workspaceId"`
	Kind          string        `bson:"kind"` // wet | copy
	Key           string        `bson:"key"`
	Mode          string        `bson:"mode,omitempty"`
	Engine        string        `bson:"engine,omitempty"`
	SourceAssetID string        `bson:"sourceAssetId"`
	SourceSHA     string        `bson:"sourceSha"`
	From          float64       `bson:"from"`
	To            float64       `bson:"to"`
	StorageKey    string        `bson:"storageKey,omitempty"`
	SHA256        string        `bson:"sha256,omitempty"`
	Size          int64         `bson:"size,omitempty"`
	Took          float64       `bson:"took,omitempty"` // seconds of processing
	Device        string        `bson:"device,omitempty"`
	Credits       float64       `bson:"credits,omitempty"`
	Guard         *Guard        `bson:"guard,omitempty"`   // wet (studio): the word check
	AssetID       string        `bson:"assetId,omitempty"` // copy: the asset
	Settings      string        `bson:"settings,omitempty"`
	Wets          []string      `bson:"wets,omitempty"` // copy: the processed voices it is made of (their keys; the receipt prices the cloud ones)
	Scores        []PersonScore `bson:"scores,omitempty"`
	CreatedAt     time.Time     `bson:"createdAt"`
	CreatedBy     models.Actor  `bson:"createdBy"`
}

func hashKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func f3(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// WetKey identifies a processed voice.
func WetKey(sourceSHA, mode, engine string, from, to float64) string {
	return hashKey("metarush.sound.wet/1", sourceSHA, mode, engine, f3(from), f3(to))
}

// CopyKey identifies a copy.
func CopyKey(sourceSHA string, start, end float64, channels int, pieces []Piece) string {
	parts := []string{"metarush.sound.copy/1", assemblyVersion, sourceSHA, f3(start), f3(end), strconv.Itoa(channels), f3(editspec.VoiceCrossfade)}
	for _, p := range pieces {
		if p.Mode == "" || p.Strength <= 0 {
			continue
		}
		parts = append(parts, f3(p.From), f3(p.To), p.Mode, f3(p.Strength), p.Wet)
	}
	return hashKey(parts...)
}

func recordID(ws, kind, key string) string { return ws + "/" + kind + ":" + key }

func findRecord(ctx context.Context, a *app.App, kind, key string) (*record, error) {
	var r record
	err := a.DB.C(Collection).FindOne(ctx, bson.M{"_id": recordID(a.WorkspaceID(), kind, key), "workspaceId": a.WorkspaceID()}).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func saveRecord(ctx context.Context, a *app.App, r *record) error {
	r.ID, r.WorkspaceID = recordID(a.WorkspaceID(), r.Kind, r.Key), a.WorkspaceID()
	if r.CreatedAt.IsZero() {
		r.CreatedAt, r.CreatedBy = time.Now().UTC(), a.Actor
	}
	_, err := a.DB.C(Collection).ReplaceOne(ctx, bson.M{"_id": r.ID}, r, options.Replace().SetUpsert(true))
	return err
}

// cachedWet loads a processed voice from the cache (nil when it is not there).
func cachedWet(ctx context.Context, a *app.App, key string) (*Wet, *record, error) {
	r, err := findRecord(ctx, a, "wet", key)
	if err != nil || r == nil {
		return nil, nil, err
	}
	p, err := a.Cache.Ensure(ctx, a.Media, r.StorageKey, r.SHA256, r.Size, nil)
	if err != nil {
		return nil, nil, nil // gone from the store: make it again
	}
	au, err := ReadWAV(p)
	if err != nil {
		return nil, nil, nil
	}
	return &Wet{Audio: au, From: r.From, Device: r.Device, Took: time.Duration(r.Took * float64(time.Second)), Guard: r.Guard}, r, nil
}

// storeWet keeps a processed voice: a 16-bit WAV, content-addressed in the
// media store and the local cache, and its record.
func storeWet(ctx context.Context, a *app.App, scratch string, r *record, w *Wet) error {
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(scratch, "wet-*.wav")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := WriteWAV(path, w.Audio, false); err != nil {
		return err
	}
	sha, err := media.HashFile(path)
	if err != nil {
		return err
	}
	key := media.ObjectKey(a.WorkspaceID(), sha)
	info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: "audio/wav", SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		return fmt.Errorf("could not store the processed voice: %w", err)
	}
	_, _ = a.Cache.Adopt(path, sha)
	r.Kind, r.StorageKey, r.SHA256, r.Size = "wet", key, sha, info.Size
	r.Took, r.Device, r.Credits, r.Guard = w.Took.Seconds(), w.Device, w.Credits, w.Guard
	return saveRecord(ctx, a, r)
}

// storeCopy turns an assembled copy into an asset (or finds the one made
// before from the same pieces) and records it.
func storeCopy(ctx context.Context, a *app.App, scratch, projectID string, source *models.Asset, r *record, au *Audio, narration bool) (*models.Asset, error) {
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratch, "copy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	title := strings.TrimSuffix(source.DisplayTitle(), filepath.Ext(source.DisplayTitle()))
	name := fmt.Sprintf("%s (clearer voice %s).wav", title, r.Key[:8])
	path := filepath.Join(dir, name)
	if err := WriteWAV(path, au, false); err != nil {
		return nil, err
	}
	tags := []string{TagEnhanced}
	if narration { // a narration take's cleaned version: no "file ready" notice, like clean-ups
		tags = append(tags, "voiceover", "cleanup:enhance")
	}
	res := ingest.File(ctx, a, path, ingest.Options{ProjectID: projectID, Tags: tags, Via: a.Actor.Via, DisplayName: name, Title: strings.TrimSuffix(name, ".wav"),
		Duplicates: ingest.DuplicatesKeep})
	if res.AssetID == "" || res.Status == "failed" || res.Status == "skipped" {
		return nil, fmt.Errorf("could not store the clearer copy: %s", res.Error)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), res.AssetID, &as); err != nil {
		return nil, err
	}
	r.Kind, r.AssetID = "copy", as.ID
	if err := saveRecord(ctx, a, r); err != nil {
		return nil, err
	}
	// A pointer from the copy's asset back to its record (scores, settings).
	if err := saveRecord(ctx, a, &record{Kind: "variant", Key: as.ID, SourceAssetID: r.SourceAssetID, SourceSHA: r.SourceSHA, From: r.From, To: r.To, Settings: r.Key}); err != nil {
		return nil, err
	}
	return &as, nil
}

// copyRecordByVariant finds the record of the copy an asset is (nil when unknown).
func copyRecordByVariant(ctx context.Context, a *app.App, variant string) *record {
	p, err := findRecord(ctx, a, "variant", variant)
	if err != nil || p == nil || p.Settings == "" {
		return nil
	}
	r, err := findRecord(ctx, a, "copy", p.Settings)
	if err != nil {
		return nil
	}
	return r
}

// soundScratch is where sound work keeps its temporary files.
func soundScratch(a *app.App) string { return filepath.Join(a.Cfg.Scratch.Dir, "sound") }

// cachedCopy finds a copy made before (with its asset still available).
func cachedCopy(ctx context.Context, a *app.App, key string) (*record, *models.Asset) {
	r, err := findRecord(ctx, a, "copy", key)
	if err != nil || r == nil || r.AssetID == "" {
		return nil, nil
	}
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), r.AssetID, &as) != nil || as.State != models.AssetAvailable {
		return nil, nil
	}
	return r, &as
}

// wetKeysOf lists the processed voices pieces use, once each.
func wetKeysOf(pieces []Piece) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pieces {
		if p.Wet != "" && !seen[p.Wet] {
			seen[p.Wet] = true
			out = append(out, p.Wet)
		}
	}
	return out
}
