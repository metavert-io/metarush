package worker

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestDevicePublishesBatteryAndPolicy(t *testing.T) {
	a := testapp.New(t)
	w := New(a)
	w.AcceptsHeavy = true
	bat := power.Battery{Present: true, Percent: 18, Source: "test"}
	w.Battery = func() power.Battery { return bat }

	d := w.device()
	if !d.DeferringHeavy || d.AcceptsHeavy || !d.HeavyEnabled || !d.OnBattery || d.BatteryPercent == nil || *d.BatteryPercent != 18 || d.HeavyBatteryThreshold != 25 {
		t.Fatalf("low battery device %+v", d)
	}
	if d.DeferReason == "" {
		t.Fatal("deferring without a reason")
	}

	bat.Charging, bat.OnAC = true, true // plugged in: big jobs run again
	d = w.device()
	if d.DeferringHeavy || !d.AcceptsHeavy || d.OnBattery || !d.Charging {
		t.Fatalf("charging device %+v", d)
	}

	bat = power.Battery{Present: true, Percent: 18, Source: "test"}
	a.Cfg.SetHeavyBatteryThreshold(0) // "Never wait"
	if d = w.device(); d.DeferringHeavy {
		t.Fatalf("threshold 0 must never defer: %+v", d)
	}
	a.Cfg.SetHeavyBatteryThreshold(25)
	w.IgnoreBattery = true // `--wait` runs jobs right away
	if d = w.device(); d.DeferringHeavy {
		t.Fatalf("an explicit --wait worker must not defer: %+v", d)
	}
}

func TestRestartContinuesTheBatteryEpisode(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	w := New(a)
	w.AcceptsHeavy = true
	w.Battery = func() power.Battery { return power.Battery{Present: true, Percent: 15, Source: "test"} }
	since := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Millisecond)
	prev := models.WorkerDevice{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "studio-mac", DeferringHeavy: true, DeferSince: &since,
		LastHeartbeat: time.Now().UTC().Add(-time.Minute)}
	if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": prev.ID}, prev, options.Replace().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	w.resumeDeferral(ctx)
	if w.deferSince == nil || !w.deferSince.Equal(since) {
		t.Fatalf("episode start %v, want %v (one notification across a restart)", w.deferSince, since)
	}

	// A machine that was off for a long time starts a new episode.
	w2 := New(a)
	w2.AcceptsHeavy = true
	w2.Battery = w.Battery
	if _, err := a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": prev.ID}, bson.M{"$set": bson.M{"lastHeartbeat": time.Now().UTC().Add(-2 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	w2.resumeDeferral(ctx)
	if w2.deferSince != nil {
		t.Fatalf("stale episode resumed: %v", w2.deferSince)
	}
}
