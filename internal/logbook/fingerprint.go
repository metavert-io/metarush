package logbook

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprints let a process that cannot read the keychain — the Claude Code
// hook, which must start in milliseconds — hide the exact configured secrets.
// Each secret is kept as its length, a 16-bit hash of its first four bytes and
// a salted SHA-256: never the value. The hook slides a window over its text,
// checks the cheap prefix hash and confirms candidates with the SHA-256.
type Fingerprints struct {
	Version int      `json:"v"`
	Salt    string   `json:"salt"`
	Items   []FPItem `json:"items"`
}

// FPItem is one secret's fingerprint.
type FPItem struct {
	N   int    `json:"n"`
	Pre uint16 `json:"p"`
	H   string `json:"h"`
}

func prefixHash(s string) uint16 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s[:4]))
	v := h.Sum32()
	return uint16(v ^ v>>16)
}

func saltedSum(salt []byte, s string) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// Fingerprints returns the fingerprints of the configured secrets (database
// password, storage keys, provider keys, the confirm-token key). Values found
// in the environment are not included: the hook scans its own environment.
func (r *Redactor) Fingerprints(salt []byte) Fingerprints {
	fp := Fingerprints{Version: 1, Salt: hex.EncodeToString(salt), Items: []FPItem{}}
	for _, v := range r.configured() {
		if len(v) < 4 {
			continue
		}
		fp.Items = append(fp.Items, FPItem{N: len(v), Pre: prefixHash(v), H: saltedSum(salt, v)})
	}
	sort.Slice(fp.Items, func(i, j int) bool { return fp.Items[i].H < fp.Items[j].H })
	return fp
}

// Redact hides every fingerprinted secret in s.
func (f *Fingerprints) Redact(s string) string {
	if f == nil || len(f.Items) == 0 || len(s) < 4 {
		return s
	}
	salt, err := hex.DecodeString(f.Salt)
	if err != nil {
		return s
	}
	type bucket struct {
		pre    map[uint16]bool
		hashes map[string]bool
	}
	byLen := map[int]*bucket{}
	var lengths []int
	for _, it := range f.Items {
		b := byLen[it.N]
		if b == nil {
			b = &bucket{pre: map[uint16]bool{}, hashes: map[string]bool{}}
			byLen[it.N] = b
			lengths = append(lengths, it.N)
		}
		b.pre[it.Pre] = true
		b.hashes[it.H] = true
	}
	sort.Sort(sort.Reverse(sort.IntSlice(lengths))) // longest first
	for _, n := range lengths {
		if len(s) < n {
			continue
		}
		b := byLen[n]
		var out strings.Builder
		last, i := 0, 0
		for i+n <= len(s) {
			if b.pre[prefixHash(s[i:i+4])] && b.hashes[saltedSum(salt, s[i:i+n])] {
				out.WriteString(s[last:i])
				out.WriteString(Redacted)
				i += n
				last = i
				continue
			}
			i++
		}
		if last > 0 {
			out.WriteString(s[last:])
			s = out.String()
		}
	}
	return s
}

// fingerprintFile is where the hook finds the fingerprints.
func fingerprintFile(hooksDir string) string { return filepath.Join(hooksDir, "redact.json") }

// WriteFingerprints saves the fingerprints of r's exact values for the hook,
// keeping the existing salt and leaving the file alone when nothing changed.
func WriteFingerprints(hooksDir string, r *Redactor) error {
	p := fingerprintFile(hooksDir)
	var cur Fingerprints
	var salt []byte
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &cur) == nil {
		salt, _ = hex.DecodeString(cur.Salt)
	}
	if len(salt) != 16 {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
	}
	next := r.Fingerprints(salt)
	if cur.Salt == next.Salt && sameItems(cur.Items, next.Items) {
		return nil
	}
	b, _ := json.Marshal(next)
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadFingerprints reads the hook's fingerprints (nil when there are none).
func LoadFingerprints(hooksDir string) *Fingerprints {
	b, err := os.ReadFile(fingerprintFile(hooksDir))
	if err != nil {
		return nil
	}
	var f Fingerprints
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return &f
}

func sameItems(a, b []FPItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
