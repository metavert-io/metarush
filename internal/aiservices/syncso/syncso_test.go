package syncso_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/syncso"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakesync"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "sk-" + strings.Repeat("Ly5", 10) + "-fake" }

func TestLipSyncConforms(t *testing.T) {
	key := fakeKey()
	f := fakesync.Start(t, key)
	a, err := aiservices.Get(syncso.Name, aiservices.KindLipSync)
	if err != nil {
		t.Fatal(err)
	}
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Key: key, Kept: f.Kept, Problems: f.Problems})
	if f.Calls("download") == 0 || f.Calls("delete-asset") != 2 {
		t.Errorf("downloads %d, uploads deleted %d", f.Calls("download"), f.Calls("delete-asset"))
	}
	if ms := f.Models(); len(ms) == 0 || !strings.Contains(ms[0], `"sync_mode":"cut_off"`) {
		t.Errorf("asked %v", ms)
	}
}

// Priced by the second of footage; the sound may not be longer than it.
func TestLipSyncPrice(t *testing.T) {
	m, err := aiservices.ModelFor(syncso.Name, aiservices.KindLipSync, "")
	if err != nil || m.ID != "lipsync-2" {
		t.Fatalf("the usual model: %v %v", m.ID, err)
	}
	m, _ = aiservices.ModelFor(syncso.Name, aiservices.KindLipSync, "lipsync-2")
	req := &aiservices.Request{Kind: aiservices.KindLipSync, Model: m.ID, Inputs: []aiservices.Media{{Role: aiservices.RoleMedia, Seconds: 12}, {Role: aiservices.RoleAudio, Seconds: 11}}}
	e, err := aiservices.EstimateFor(syncso.Name, m, req, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	if err != nil || e.USD != 0.6 || e.Units != "12 seconds of footage" {
		t.Fatalf("estimate %+v %v", e, err)
	}
	req.Inputs[1].Seconds = 20
	if err := m.Check(req, time.Now()); err == nil || !strings.Contains(err.Error(), "longer than the footage") {
		t.Errorf("longer sound: %v", err)
	}
}

// sync.'s refusals in plain words: the plan, a refusal, a failed match.
func TestLipSyncRefusals(t *testing.T) {
	key := fakeKey()
	f := fakesync.Start(t, key)
	a, _ := aiservices.Get(syncso.Name, aiservices.KindLipSync)
	m, _ := aiservices.ModelFor(syncso.Name, aiservices.KindLipSync, "lipsync-2")
	req := aiconformance.SampleRequest(t, m, t.TempDir())
	ctx := context.Background()
	f.Script(fakesync.Behavior{Kind: fakesync.Plan})
	var ve *videogen.Error
	if _, err := a.Submit(ctx, key, req); !errors.As(err, &ve) || ve.Kind != videogen.KindPermission || !strings.Contains(ve.Message, "plan") {
		t.Errorf("plan: %v", err)
	}
	for kind, want := range map[string]string{fakesync.Rejected: videogen.KindSafety, fakesync.Fail: videogen.KindServer} {
		f.Script(fakesync.Behavior{Kind: kind})
		tk, err := a.Submit(ctx, key, req)
		if err != nil {
			t.Fatal(err)
		}
		var st *aiservices.Status
		for i := 0; i < 5; i++ {
			if st, err = a.Poll(ctx, key, tk); err != nil || st.State != aiservices.StateRunning {
				break
			}
		}
		if err != nil || st.State != aiservices.StateFailed || st.Err == nil || st.Err.Kind != want {
			t.Errorf("%s: %+v %v", kind, st, err)
		}
	}
	t.Setenv(syncso.EnvBaseURL, "")
	if _, err := syncso.BaseURL(); err == nil {
		t.Error("the real host under go test")
	}
}

// sync.'s name ends in a period: a sentence ending with it has just the one.
func TestSentencesEndingInItsName(t *testing.T) {
	key := fakeKey()
	fakesync.Start(t, key)
	ctx := context.Background()
	p, err := providers.Get(syncso.Name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Test(ctx, key)
	if err != nil || !r.OK || strings.Contains(r.Message, "..") {
		t.Errorf("connected: %+v %v", r, err)
	}
	var ve *videogen.Error
	if _, err := (&syncso.Adapter{}).Fetch(ctx, "", nil, &aiservices.Output{URI: "https://example.com/out.mp4"}, io.Discard); !errors.As(err, &ve) || strings.Contains(ve.Message, "..") {
		t.Errorf("a download elsewhere: %v", err)
	}
	t.Setenv(syncso.EnvBaseURL, "http://127.0.0.1:1") // nothing listens there
	a, _ := aiservices.Get(syncso.Name, aiservices.KindLipSync)
	if _, err := a.Poll(ctx, key, &aiservices.Ticket{ID: "gen-1"}); !errors.As(err, &ve) || ve.Kind != videogen.KindNetwork || strings.Contains(ve.Message, "..") {
		t.Errorf("unreachable: %v", err)
	}
}
