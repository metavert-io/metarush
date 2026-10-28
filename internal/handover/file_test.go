package handover_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A USB drive formatted exFAT has no hard links: the file is still made there
// (never over another), private, with nothing half-written left beside it.
func TestHandoverFileOnADriveWithoutHardLinks(t *testing.T) {
	defer handover.NoHardLinks()()
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "Hello.")
	drive := t.TempDir()
	out := filepath.Join(drive, "h.age")
	pctx := handover.WithPassphrase(ctx, pass)
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", st, err)
	}
	if left, _ := filepath.Glob(filepath.Join(drive, ".metarush-handover-*")); len(left) != 0 {
		t.Fatalf("left beside it: %v", left)
	}
	// Never over a file that is there, even without hard links.
	_, err := ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	refused(t, err, apperr.Conflict, "")
	nu.at(t, remembers())
	if r := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out}); r.Moved.Conversations != 1 {
		t.Fatalf("receive %+v", r.Moved)
	}
}

// A place the file can't be written is refused before anything is marked or
// recorded; a file that fails part way puts everything back.
func TestNothingMovesWhenTheFileCantBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	w := newWorkspace(t)
	old, _ := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	m := old.at(t, remembers())
	id, _ := talk(t, m, "", "Hello.")
	pctx := handover.WithPassphrase(ctx, pass)
	nothingMoved := func(what string) {
		t.Helper()
		if n, _ := w.db.C(store.CHandovers).CountDocuments(ctx, bson.M{"workspaceId": w.id, "active": true}); n != 0 {
			t.Fatalf("%s: a handover is in progress", what)
		}
		if d := conv(t, old.a, id); d.HandingOver != nil {
			t.Fatalf("%s: the conversation is marked", what)
		}
	}

	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	_, err := ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(ro, "h.age")}, person())
	refused(t, err, apperr.Conflict, "not_writable")
	if n, _ := w.db.C(store.CHandovers).CountDocuments(ctx, bson.M{"workspaceId": w.id}); n != 0 {
		t.Fatal("a handover was recorded")
	}
	nothingMoved("read-only folder")

	// A file in the conversation's folder that can't be read: the file fails
	// after the conversations were marked, and they are put back.
	bad := filepath.Join(assistant.ConversationFolder(old.a, id), "unreadable.txt")
	if err := os.WriteFile(bad, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "h.age")
	_, err = ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	if err == nil || !strings.Contains(err.Error(), "Nothing moved") {
		t.Fatalf("export with an unreadable file: %v", err)
	}
	if _, serr := os.Stat(out); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("a half-made file is there")
	}
	nothingMoved("unreadable file")
	_ = os.Remove(bad)
	if _, reply := talk(t, m, id, "Still here?"); reply != "I remember 1 messages." {
		t.Fatalf("it goes on here: %q", reply)
	}
	// And a handover can be made after.
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
}

type tarEntry struct {
	name string
	body []byte
}

func readPlain(t *testing.T, file string) []tarEntry {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out []tarEntry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out = append(out, tarEntry{h.Name, b})
	}
}

func writePlain(t *testing.T, file string, es []tarEntry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(e.body)
	}
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Receiving reads the file's list first — whose it is, for which computer,
// how big each file is — and unpacks nothing it doesn't list, nothing bigger
// than the list says and nothing that differs from it.
func TestReceiveReadsTheListFirst(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "Hello.")
	good := filepath.Join(t.TempDir(), "h.tar.gz")
	call[handover.HandoverExportOut](t, ctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: good, NoPassphrase: true})
	es := readPlain(t, good)
	if es[0].name != "handover.json" || len(es) < 3 {
		t.Fatalf("the list isn't first: %d entries, first %s", len(es), es[0].name)
	}
	nu.at(t, remembers())
	dir := t.TempDir()
	try := func(name string, change func(es []tarEntry) []tarEntry) error {
		cp := make([]tarEntry, len(es))
		for i, e := range es {
			cp[i] = tarEntry{e.name, append([]byte(nil), e.body...)}
		}
		f := filepath.Join(dir, name)
		writePlain(t, f, change(cp))
		_, err := ops.CallTyped[handover.HandoverReceiveOut](ctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: f}, person())
		return err
	}
	manifest := func(es []tarEntry, edit func(m map[string]any)) {
		var m map[string]any
		if err := json.Unmarshal(es[0].body, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		es[0].body, _ = json.Marshal(m)
	}
	// For another computer, and with a file far bigger than listed after the
	// list: refused for whose it is, before anything is unpacked.
	refused(t, try("other.tar.gz", func(es []tarEntry) []tarEntry {
		manifest(es, func(m map[string]any) {
			m["to"] = map[string]any{"id": "ins_01k8someoneelse0000000000", "name": "kitchen"}
		})
		es[1].body = bytes.Repeat([]byte("x"), 1<<20)
		return es
	}), apperr.Conflict, "other_computer")
	refused(t, try("last.tar.gz", func(es []tarEntry) []tarEntry {
		return append(es[1:], es[0])
	}), apperr.InvalidInput, "damaged")
	refused(t, try("bigger.tar.gz", func(es []tarEntry) []tarEntry {
		es[1].body = append(es[1].body, []byte("more")...)
		return es
	}), apperr.InvalidInput, "damaged")
	refused(t, try("changed.tar.gz", func(es []tarEntry) []tarEntry {
		es[1].body[0] ^= 1
		return es
	}), apperr.InvalidInput, "damaged")
	refused(t, try("extra.tar.gz", func(es []tarEntry) []tarEntry {
		return append(es, tarEntry{"sessions/x/extra.jsonl", []byte("{}")})
	}), apperr.InvalidInput, "damaged")
	refused(t, try("missing.tar.gz", func(es []tarEntry) []tarEntry {
		return es[:len(es)-1]
	}), apperr.InvalidInput, "damaged")
	// Nothing was received, and the honest file still is.
	if d := conv(t, nu.a, mustOne(t, old)); d.InstallationID != old.a.Cfg.Installation.ID {
		t.Fatal("a refused file moved a conversation")
	}
	if r := call[handover.HandoverReceiveOut](t, ctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: good}); r.Moved.Conversations != 1 {
		t.Fatalf("receive %+v", r.Moved)
	}
}

func mustOne(t *testing.T, c *computer) string {
	t.Helper()
	var d assistant.Conversation
	if err := c.a.DB.C(store.CAssistantConversations).FindOne(context.Background(), bson.M{"workspaceId": c.a.WorkspaceID()}).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d.ID
}
