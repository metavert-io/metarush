package api

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/uploads"
)

// The dashboard starts on its own port and stops when asked; when something
// already uses the port it says so plainly, with what to do.
func TestTheDashboardStartsAndStops(t *testing.T) {
	d, _, _ := newDash(t, noClaude)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	d.a.Cfg.Dashboard.Host = "127.0.0.1"
	d.a.Cfg.Dashboard.Port = busy.Addr().(*net.TCPAddr).Port
	err = d.srv.Listen(context.Background())
	ae := apperr.From(err)
	if err == nil || !strings.Contains(ae.Message, "already using port "+strconv.Itoa(d.a.Cfg.Dashboard.Port)) || ae.Command() != "metarush stop" || len(plainwords.Check(ae.Message)) > 0 {
		t.Fatalf("a port in use: %v", err)
	}
	// A free port: it serves until stopped.
	d.a.Cfg.Dashboard.Port = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.srv.Listen(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the dashboard didn't stop")
	}
}

// Uploads a restart interrupted while they were being added: the ones whose
// bytes are all there are added again; one whose bytes went missing says so
// and asks for the file again.
func TestUploadsInterruptedByARestart(t *testing.T) {
	needFFmpeg(t)
	d, projectID, _ := newDash(t, noClaude)
	clip, err := os.ReadFile(clipFile(t, "restart.mp4", 2, true))
	if err != nil {
		t.Fatal(err)
	}
	u := d.srv.uploads
	ingesting := func(name string, size int64) uploads.Record {
		t.Helper()
		rec, err := u.st.Create(name, size, projectID, "")
		if err != nil {
			t.Fatal(err)
		}
		rec, _ = u.st.Update(rec.ID, true, func(x *uploads.Record) {
			x.State, x.UserID, x.UserName = uploads.StateIngesting, d.a.Cfg.User.ID, d.a.Cfg.User.Name
		})
		return rec
	}
	whole := ingesting("whole.mp4", int64(len(clip)))
	if err := os.WriteFile(u.st.Part(whole.ID), clip, 0o600); err != nil {
		t.Fatal(err)
	}
	partial := ingesting("partial.mp4", int64(len(clip)))
	if err := os.WriteFile(u.st.Part(partial.ID), clip[:len(clip)/3], 0o600); err != nil {
		t.Fatal(err)
	}
	u.recover()
	if rec, _ := u.st.Get(partial.ID); rec.State != uploads.StateFailed || rec.Error != "The part of the file MetaRush had received is missing. Drop the file again." {
		t.Fatalf("missing bytes: %+v", rec)
	}
	end := time.Now().Add(60 * time.Second)
	for {
		rec, _ := u.st.Get(whole.ID)
		if rec.State == uploads.StateDone {
			if rec.Result == nil || rec.Result.AssetID == "" {
				t.Fatalf("added: %+v", rec)
			}
			if _, err := os.Stat(u.st.Part(whole.ID)); !os.IsNotExist(err) {
				t.Fatal("the received part is kept after the file was added")
			}
			break
		}
		if rec.State == uploads.StateFailed || time.Now().After(end) {
			t.Fatalf("the whole upload: %+v", rec)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
