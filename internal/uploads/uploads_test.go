package uploads

import (
	"os"
	"testing"
	"time"
)

func TestFindResumableMatchesTheSameFile(t *testing.T) {
	s := For(t.TempDir())
	fp := "fp1:" + "ab12"
	a, err := s.Create("clip.mov", 5000, "", fp)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Create("clip.mov", 5000, "", fp)
	setOffset(t, s, a.ID, 1000)
	setOffset(t, s, b.ID, 3000)
	other, _ := s.Create("clip.mov", 5000, "", "fp1:ffff") // same name and size, different file
	setOffset(t, s, other.ID, 4000)
	done, _ := s.Create("clip.mov", 5000, "", fp)
	s.Update(done.ID, true, func(r *Record) { r.Offset, r.State = 5000, StateDone })

	// A re-dropped file (a folder path from the browser) resumes the furthest
	// matching upload that is still receiving.
	got, ok := s.FindResumable("Trip/clip.mov", 5000, fp)
	if !ok || got.ID != b.ID || got.Offset != 3000 {
		t.Fatalf("resumable = %+v, %v; want %s at 3000", got, ok, b.ID)
	}
	if _, ok := s.FindResumable("clip.mov", 4999, fp); ok {
		t.Fatal("a different size must not resume")
	}
	if _, ok := s.FindResumable("other.mov", 5000, fp); ok {
		t.Fatal("a different name must not resume")
	}
	// Without a fingerprint on one side, name and size decide (the furthest wins).
	if got, ok := s.FindResumable("clip.mov", 5000, ""); !ok || got.ID != other.ID {
		t.Fatalf("no fingerprint: %+v %v", got, ok)
	}

	// Records survive a restart (a new process reads them from disk).
	s.Flush()
	regMu.Lock()
	delete(registry, s.dir)
	regMu.Unlock()
	again := For(s.dir[:len(s.dir)-len("/uploads")])
	if got, ok := again.FindResumable("clip.mov", 5000, fp); !ok || got.ID != b.ID || got.Offset != 3000 {
		t.Fatalf("after reload: %+v %v", got, ok)
	}
	if _, err := os.Stat(again.Part(b.ID)); err != nil {
		t.Fatalf("part file: %v", err)
	}
}

// setOffset writes n received bytes to the part file (the source of truth after
// a restart) and records the offset, as the PATCH handler does.
func setOffset(t *testing.T, s *Store, id string, n int64) {
	t.Helper()
	if err := os.WriteFile(s.Part(id), make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Update(id, true, func(r *Record) { r.Offset = n })
}

func TestListHidesDismissedAndOldFinished(t *testing.T) {
	s := For(t.TempDir())
	live, _ := s.Create("a.mov", 10, "", "")
	old, _ := s.Create("b.mov", 10, "", "")
	s.Update(old.ID, true, func(r *Record) { r.State = StateDone })
	s.mu.Lock()
	s.m[old.ID].UpdatedAt = time.Now().Add(-time.Hour) // finished an hour ago
	s.mu.Unlock()
	gone, _ := s.Create("c.mov", 10, "", "")
	s.Update(gone.ID, true, func(r *Record) { r.State, r.Dismissed = StateDone, true })
	fresh, _ := s.Create("d.mov", 10, "", "")
	s.Update(fresh.ID, true, func(r *Record) { r.State = StateDone })

	ids := map[string]bool{}
	for _, r := range s.List(30 * time.Minute) {
		ids[r.ID] = true
	}
	if !ids[live.ID] || !ids[fresh.ID] || ids[old.ID] || ids[gone.ID] {
		t.Fatalf("listed %v", ids)
	}
}

func TestInterruptedAndProgress(t *testing.T) {
	now := time.Now()
	r := &Record{State: StateReceiving, Size: 1000, Offset: 250, LastProgressAt: now.Add(-InterruptedAfter - time.Second)}
	if !r.Interrupted(now) || r.Pct() != 0.25 {
		t.Fatalf("interrupted %v pct %v", r.Interrupted(now), r.Pct())
	}
	r.LastProgressAt = now
	if r.Interrupted(now) {
		t.Fatal("fresh progress is not interrupted")
	}
	r.Disconnected = true // the last chunk was cut off (tab closed or reloaded)
	if !r.Interrupted(now) {
		t.Fatal("a cut-off chunk should read as interrupted at once")
	}

	if StageFor("probe") != StageChecking || StageFor("hash") != StageFingerprinting || StageFor("upload") != StageStoring || StageFor("cache") != StageStoring {
		t.Fatal("stage mapping")
	}
	p := &Record{}
	t0 := time.Unix(1_700_000_000, 0)
	Progress(p, StageStoring, 0, 4000, t0)
	Progress(p, StageStoring, 1000, 4000, t0.Add(time.Second))
	if p.Stage != StageStoring || p.StageDone != 1000 || p.StageTotal != 4000 || p.Rate < 999 || p.Rate > 1001 {
		t.Fatalf("progress %+v", p)
	}
	// A new stage starts its own count and rate.
	Progress(p, StageDone, 0, 0, t0.Add(2*time.Second))
	if p.StageDone != 0 || p.Rate != 0 || !p.StageStartedAt.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("new stage %+v", p)
	}
}
