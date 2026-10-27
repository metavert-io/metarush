package browserext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	originA = "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	originB = "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// Pairings are a person's (D197): each person's code waits on its own, the
// pairing is the code's person's, a wrong code counts against every waiting
// code (each burns after five), and a person's browsers are unpaired together.
func TestPairingsArePeoples(t *testing.T) {
	s := For(t.TempDir()).Owned(Person{ID: "usr_owner", Name: "Sam"})
	now := time.Now()
	maya := Person{ID: "usr_maya", Name: "Maya"}
	codeJ, _, err := s.NewCode(now, maya)
	if err != nil {
		t.Fatal(err)
	}
	codeO, _, _ := s.NewCode(now, Person{ID: "usr_owner", Name: "Sam"})
	// A fresh code of hers replaces her waiting one, not the owner's.
	codeJ2, _, _ := s.NewCode(now, maya)
	if _, _, err := s.Pair(codeJ, originA, "Chrome", "1", now); err != ErrBadCode {
		t.Fatalf("her replaced code paired: %v", err)
	}
	if ok, _ := s.Pending(now, "usr_owner"); !ok {
		t.Fatal("the owner's code stopped waiting")
	}
	_, b, err := s.Pair(codeJ2, originA, "Chrome", "1", now)
	if err != nil || b.UserID != "usr_maya" || b.UserName != "Maya" {
		t.Fatalf("her browser: %+v %v", b, err)
	}
	if ok, _ := s.Pending(now, "usr_maya"); ok {
		t.Fatal("her used code still waits")
	}
	// Four more wrong tries (five with the one above) burn the owner's code.
	for i := 0; i < 4; i++ {
		_, _, _ = s.Pair("ZZZZ-ZZZZ", originB, "Edge", "1", now)
	}
	if _, _, err := s.Pair(codeO, originB, "Edge", "1", now); err != ErrNoCode && err != ErrBadCode {
		t.Fatalf("a code survived five wrong tries: %v", err)
	}
	codeO2, _, _ := s.NewCode(now, Person{ID: "usr_owner", Name: "Sam"})
	if _, b, err := s.Pair(codeO2, originB, "Edge", "1", now); err != nil || b.UserID != "usr_owner" {
		t.Fatalf("the owner's browser: %+v %v", b, err)
	}
	if mine := s.ListFor("usr_maya"); len(mine) != 1 || mine[0].Origin != originA {
		t.Fatalf("hers: %+v", mine)
	}
	if n, err := s.RevokeFor("usr_maya"); err != nil || n != 1 || len(s.List()) != 1 || s.List()[0].UserID != "usr_owner" {
		t.Fatalf("unpairing hers: %d %v %+v", n, err, s.List())
	}
}

// A version-1 file (pairings and a waiting code without a person) becomes the
// owner's the first time it is read with the owner known.
func TestPairingsFromBeforeMoveToTheOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "browser-extension.json")
	exp := time.Now().Add(5 * time.Minute).UTC()
	old := map[string]any{
		"browsers": []map[string]any{{"id": "ext_old", "name": "Chrome", "origin": originA, "pairedAt": time.Now().UTC(), "tokenHash": hash("mrx_x")}},
		"code":     map[string]any{"hash": hash(normalizeCode("K7QD-3MXA")), "expiresAt": exp, "tries": 0},
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s := For(dir).Owned(Person{ID: "usr_owner", Name: "Sam"})
	list := s.List()
	if len(list) != 1 || list[0].UserID != "usr_owner" || list[0].UserName != "Sam" {
		t.Fatalf("after the move: %+v", list)
	}
	var st state
	after, _ := os.ReadFile(path)
	_ = json.Unmarshal(after, &st)
	if st.Version != stateVersion || st.Code != nil || len(st.Codes) != 1 || st.Codes[0].UserID != "usr_owner" {
		t.Fatalf("the file: %s", after)
	}
	if _, b, err := s.Pair("K7QD-3MXA", originB, "Edge", "1", time.Now()); err != nil || b.UserID != "usr_owner" {
		t.Fatalf("the code from before: %+v %v", b, err)
	}
}
