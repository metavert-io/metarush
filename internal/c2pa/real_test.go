//go:build c2pareal

package c2pa

// The real c2patool, once by hand on a computer that has it (never in CI,
// never downloaded by a test): a short video ffmpeg makes, signed through
// MetaRush's own signer with a test certificate and a brought-style chain of
// each key kind, read back (the copy carrying exactly the signature MetaRush
// made), the signed file searched for names that must never be in it; and the
// reviewer's attack on a signing in progress (D436).
//
//	METARUSH_REAL_C2PATOOL=/path/to/c2patool go test -tags c2pareal -run TestRealC2patool -v ./internal/c2pa/

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/testutil/fakec2patool"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestRealC2patool(t *testing.T) {
	bin := os.Getenv("METARUSH_REAL_C2PATOOL")
	if bin == "" {
		t.Skip("METARUSH_REAL_C2PATOOL names no c2patool")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ts := tools.New(config.Tools{C2PATool: bin})
	tool := Tool{Tools: ts, Dir: filepath.Join(t.TempDir(), "private")}
	ctx := context.Background()
	if why := tool.Ready(ctx); why != "" {
		t.Fatal(why)
	}
	t.Logf("c2patool %s", ts.Version(ctx, tools.C2PATool))
	dir := t.TempDir()
	in := filepath.Join(dir, "acme-launch_teaser_r3_landscape-1080p_2s.mp4")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=navy:s=320x240:r=30:d=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", in).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	def, err := Manifest{Version: "0.16.0-dev", Parts: []Part{
		{What: "footage", SHA256: strings.Repeat("1", 64), Format: "video/quicktime"},
		{What: "footage", SHA256: strings.Repeat("2", 64), Format: "video/mp4", AI: AIMade, Made: "Google, veo-3.1"},
		{What: "voice", SHA256: strings.Repeat("3", 64), Format: "audio/mpeg", AI: AIMade, Made: "ElevenLabs, eleven_multilingual_v2"},
		{What: "lip-sync", SHA256: strings.Repeat("4", 64), Format: "video/mp4", AI: AIEdited, Made: "sync., lipsync-2"},
		{What: "picture", SHA256: strings.Repeat("5", 64), Format: "image/png", AI: AIEnhanced, Made: "this computer, background removal"},
		{What: "music", SHA256: strings.Repeat("6", 64), Format: "audio/wav"},
	}}.Definition()
	if err != nil {
		t.Fatal(err)
	}
	test, _ := MakeTest("", time.Now())
	creds := map[string]*Credential{"test certificate": test}
	for _, k := range []string{"p256", "p384", "rsa", "ed25519"} {
		creds["brought "+k] = credentialWith(t, k)
	}
	creds["brought, document signing only"] = credentialWith(t, "p256", func(c *x509.Certificate) {
		c.ExtKeyUsage, c.UnknownExtKeyUsage = nil, append(c.UnknownExtKeyUsage, oidDocumentSigning)
	})
	creds["brought, C2PA claim signing only"] = credentialWith(t, "p256", func(c *x509.Certificate) {
		c.ExtKeyUsage, c.UnknownExtKeyUsage = nil, append(c.UnknownExtKeyUsage, oidClaimSigning)
	})
	for name, cred := range creds {
		out := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".mp4")
		s, err := tool.Sign(ctx, SignIn{Video: in, Manifest: def, Credential: cred})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !s.Verified || s.Signatures != 1 {
			t.Fatalf("%s: %+v (%s)", name, s, s.Problem)
		}
		if err := os.Link(s.Path, out); err != nil {
			t.Fatal(err)
		}
		s.Remove()
		h, err := ManifestHash(out)
		if err != nil || h != s.ManifestHash {
			t.Fatalf("%s: manifest hash %s %v", name, h, err)
		}
		r := s.Report
		if r.State != "Valid" || r.Title != Title || r.Generator != "MetaRush 0.16.0-dev" || r.Ingredients != 6 {
			t.Fatalf("%s: report %+v", name, r)
		}
		for _, want := range []string{SourceSynthetic, SourceTrained, SourceAIEdited, SourceEnhanced} {
			if !strings.Contains(strings.Join(r.SourceTypes, " "), want) {
				t.Errorf("%s: no %s in %v", name, want, r.SourceTypes)
			}
		}
		b, _ := os.ReadFile(out)
		for _, never := range []string{"acme", "teaser", "launch", "landscape"} {
			if bytes.Contains(bytes.ToLower(b), []byte(never)) {
				t.Errorf("%s: the signed file says %q", name, never)
			}
		}
		t.Logf("%s: %s, signed by %q (issuer %q), manifest %s…", name, r.State, r.Signer.CommonName, r.Signer.Issuer, h[:16])
	}
	// The detailed report: actions and ingredients are created assertions.
	det, err := ts.Run(ctx, tools.Spec{Tool: tools.C2PATool, Args: []string{filepath.Join(dir, "test-certificate.mp4"), "-d"}, Env: cleanEnv(dir), CleanEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Active    string `json:"active_manifest"`
		Manifests map[string]struct {
			Claim struct {
				Created  []struct{ URL string } `json:"created_assertions"`
				Gathered []struct{ URL string } `json:"gathered_assertions"`
			} `json:"claim"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(det.Stdout, &d); err != nil {
		t.Fatal(err)
	}
	c := d.Manifests[d.Active].Claim
	if len(c.Gathered) != 0 || len(c.Created) != 8 {
		t.Fatalf("created %v, gathered %v", c.Created, c.Gathered)
	}
	// A byte changed in the video after signing doesn't check out.
	b, _ := os.ReadFile(filepath.Join(dir, "test-certificate.mp4"))
	i := bytes.Index(b, []byte("mdat"))
	b[i+200] ^= 0xff
	bad := filepath.Join(dir, "changed.mp4")
	_ = os.WriteFile(bad, b, 0o644)
	r, err := tool.Read(ctx, bad)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := r.Check(test); ok || !strings.Contains(why, "the video's bytes don't match what was signed") {
		t.Fatalf("changed copy: %v %q", ok, why)
	}
	if r, err := tool.Read(ctx, in); err != nil || !r.None {
		t.Fatalf("the unsigned video: %+v %v", r, err)
	}
}

// The reviewer's attack with the real c2patool (D436): c2patool waits (a
// wrapper that pauses, then becomes c2patool with the same process, environment
// and descriptors) while this test reads its environment and arguments as any
// program of the person's can, looks for a socket to the signer and runs the
// signer from outside; then it signs, with exactly one signature.
func TestRealC2patoolIsNoOracle(t *testing.T) {
	bin := os.Getenv("METARUSH_REAL_C2PATOOL")
	if bin == "" {
		t.Skip("METARUSH_REAL_C2PATOOL names no c2patool")
	}
	dir := t.TempDir()
	hold := filepath.Join(dir, "hold")
	_ = os.WriteFile(hold, nil, 0o600)
	// The stand-in waits, then becomes the real c2patool (the same process).
	wrapper, _ := fakec2patool.Write(t, fakec2patool.State{Hold: hold, Exec: bin})
	tool := Tool{Tools: tools.New(config.Tools{C2PATool: wrapper}), Dir: filepath.Join(dir, "private")}
	cred, _ := MakeTest("", time.Now())
	def, _ := Manifest{Version: "0.16.0-dev", Parts: []Part{{What: "footage", SHA256: strings.Repeat("1", 64), Format: "video/mp4"}}}.Definition()
	in := filepath.Join(dir, "in.mp4")
	if err := WriteSample(in); err != nil {
		t.Fatal(err)
	}
	type result struct {
		s   *Signed
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := tool.Sign(context.Background(), SignIn{Video: in, Manifest: def, Credential: cred})
		done <- result{s, err}
	}()
	pid := 0
	for deadline := time.Now().Add(time.Minute); pid == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("c2patool never started")
		}
		if b, err := os.ReadFile(hold + ".ready"); err == nil {
			_, _ = fmt.Sscan(string(b), &pid)
		}
	}
	seen := processSays(t, pid)
	t.Logf("what c2patool's process shows:\n%s", seen)
	for _, never := range []string{"METARUSH_C2PA", "TICKET", "SOCKET", ".sock"} {
		if strings.Contains(strings.ToUpper(seen), strings.ToUpper(never)) {
			t.Errorf("c2patool's environment or arguments carry %q", never)
		}
	}
	_ = filepath.WalkDir(tool.Dir, func(p string, d os.DirEntry, err error) error {
		if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSocket != 0 {
			t.Errorf("a socket to the signer: %s", p)
		}
		return nil
	})
	exe, _ := os.Executable()
	cmd := exec.Command(exe, SignerCommand)
	cmd.Stdin = bytes.NewReader(append(append([]byte(nil), sigStructure...), `{"claim":"the attacker's"}`...))
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), errOutsider) {
		t.Errorf("the signer run from outside: %q %v", out, err)
	}
	_ = os.Remove(hold)
	res := <-done
	if res.err != nil || !res.s.Verified || res.s.Signatures != 1 {
		t.Fatalf("the signing: %+v %v", res.s, res.err)
	}
	res.s.Remove()
	t.Log("nothing reached the signer from outside; the signing carries its one signature")
}
