package c2pa

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/testutil/fakec2patool"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	MaybeSigner()           // c2patool's signer is this binary
	fakec2patool.MaybeRun() // and so is the stand-in c2patool
	os.Exit(m.Run())
}

// fakeTool is a c2patool stand-in with its state and the log of its calls;
// its signings work in a private folder of the test's.
func fakeTool(t *testing.T, st fakec2patool.State) (Tool, string) {
	t.Helper()
	st.Log = filepath.Join(t.TempDir(), "calls.jsonl")
	tool, _ := fakec2patool.Write(t, st)
	return Tool{Tools: tools.New(config.Tools{C2PATool: tool}), Dir: filepath.Join(t.TempDir(), "private")}, st.Log
}

// leftIn lists what is left in a tool's private folder.
func leftIn(t *testing.T, tool Tool) []string {
	t.Helper()
	es, _ := os.ReadDir(tool.Dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// signingDir is the private folder a logged signing worked in (its manifest's).
func signingDir(c fakec2patool.Call) string {
	for i, a := range c.Args {
		if a == "--manifest" && i+1 < len(c.Args) {
			return filepath.Dir(c.Args[i+1])
		}
	}
	return ""
}

func video(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "render.mp4")
	if err := os.WriteFile(p, mp4Bytes(strings.Repeat("frame ", 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// mp4Bytes is a well-formed little MP4's boxes: ftyp, then mdat holding payload.
func mp4Bytes(payload string) []byte {
	b := []byte("\x00\x00\x00\x10ftypisom\x00\x00\x00\x00")
	n := 8 + len(payload)
	b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	b = append(b, "mdat"...)
	return append(b, payload...)
}

func definition(t *testing.T) []byte {
	t.Helper()
	b, err := Manifest{Version: "0.16.0", Parts: []Part{
		{What: "footage", SHA256: strings.Repeat("a", 64), Format: "video/quicktime"},
		{What: "footage", SHA256: strings.Repeat("b", 64), Format: "video/mp4", AI: AIMade, Made: "Google Veo 3.1"},
	}}.Definition()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// keyForms are every written form of a credential's private key.
func keyForms(t *testing.T, c *Credential) []string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(c.Key)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(der)
	forms := []string{b64, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	for i := 0; i+40 <= len(b64); i += 40 {
		forms = append(forms, b64[i:i+40])
	}
	if k, ok := c.Key.(*ecdsa.PrivateKey); ok {
		forms = append(forms, k.D.Text(16), k.D.String())
	}
	return forms
}

func TestSignThroughMetaRushsOwnSigner(t *testing.T) {
	cred, err := MakeTest("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tool, log := fakeTool(t, fakec2patool.State{})
	in := video(t)
	s, err := tool.Sign(context.Background(), SignIn{Video: in, Manifest: definition(t), Credential: cred})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Verified || s.Problem != "" || s.Signatures != 1 || filepath.Dir(filepath.Dir(s.Path)) != tool.Dir {
		t.Fatalf("signed: %+v", s)
	}
	out := s.Path
	if st, err := os.Stat(filepath.Dir(out)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the signing's folder: %v %v", st.Mode(), err)
	}
	h, err := ManifestHash(out)
	if err != nil || h != s.ManifestHash || len(h) != 64 {
		t.Fatalf("manifest hash %q %v, signed says %q", h, err, s.ManifestHash)
	}
	if s.Report.Signer.Serial != cred.Leaf().SerialNumber.String() || s.Report.Title != Title || s.Report.Generator != "MetaRush 0.16.0" {
		t.Fatalf("report: %+v", s.Report)
	}
	if got := strings.Join(s.Report.SourceTypes, " "); !strings.Contains(got, SourceSynthetic) || !strings.Contains(got, SourceTrained) {
		t.Fatalf("source types: %v", s.Report.SourceTypes)
	}
	// The key reached no command line and no environment: not c2patool's, not its signer's.
	calls := fakec2patool.Calls(log)
	if len(calls) < 2 {
		t.Fatalf("calls: %d", len(calls))
	}
	all, _ := json.Marshal(calls)
	for _, f := range keyForms(t, cred) {
		if strings.Contains(string(all), f) {
			t.Fatalf("the key is in c2patool's arguments or environment: %.20s…", f)
		}
	}
	// Nothing that leads to the signer either: no socket, no ticket.
	for _, c := range calls {
		for _, e := range c.Env {
			k, _, _ := strings.Cut(e, "=")
			if strings.HasPrefix(k, "C2PA_") || k == "C2PATOOL_SETTINGS" || k == secretpath.SigningKeyEnv || strings.HasPrefix(k, "METARUSH_C2PA") {
				t.Errorf("c2patool got %s", k)
			}
		}
	}
	// The signed copy carries exactly MetaRush's signature, and the folder
	// c2patool worked in goes with it once the copy is placed.
	store, err := manifestStore(out)
	if err != nil {
		t.Fatal(err)
	}
	if sigs, err := StoreSignatures(store); err != nil || len(sigs) != 1 {
		t.Fatalf("the copy's signatures: %d %v", len(sigs), err)
	}
	dir := signingDir(calls[0])
	if dir != filepath.Dir(out) {
		t.Fatalf("c2patool worked in %s, the copy is in %s", dir, out)
	}
	s.Remove()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the signing's folder was left: %s (%v)", dir, err)
	}
	// (Reading the copy back after this works on a copy of it.)
	out = filepath.Join(t.TempDir(), "signed.mp4")
	s2, err := tool.Sign(context.Background(), SignIn{Video: in, Manifest: definition(t), Credential: cred})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(s2.Path, out); err != nil {
		t.Fatal(err)
	}
	s2.Remove()
	if left := leftIn(t, tool); len(left) != 0 {
		t.Fatalf("left in the private folder: %v", left)
	}
	// Reading the signed copy says the same; the render itself carries nothing.
	rep, err := tool.Read(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := rep.Check(cred); !ok {
		t.Fatal(why)
	}
	if rep, err := tool.Read(context.Background(), in); err != nil || !rep.None {
		t.Fatalf("the render: %+v %v", rep, err)
	}
	if _, err := ManifestHash(in); err != ErrNoManifest {
		t.Fatalf("no manifest in the render: %v", err)
	}
}

func TestACopyThatDoesntCheckOut(t *testing.T) {
	cred, _ := MakeTest("", time.Now())
	tool, _ := fakeTool(t, fakec2patool.State{TamperAfterSign: true})
	s, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Remove()
	if s.Verified || !strings.Contains(s.Problem, "the video's bytes don't match what was signed") {
		t.Fatalf("tampered copy: %+v", s)
	}
}

func TestAC2patoolThatSignsByItselfIsRefused(t *testing.T) {
	cred, _ := MakeTest("", time.Now())
	tool, _ := fakeTool(t, fakec2patool.State{OwnKey: true})
	_, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred})
	if err == nil || !strings.Contains(err.Error(), "without MetaRush signing them") {
		t.Fatalf("own key: %v", err)
	}
	if left := leftIn(t, tool); len(left) != 0 {
		t.Fatalf("the copy it signed by itself was kept: %v", left)
	}
}

func TestSignFailsPlainly(t *testing.T) {
	cred, _ := MakeTest("", time.Now())
	tool, _ := fakeTool(t, fakec2patool.State{FailSign: "the certificate is invalid"})
	_, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred})
	if err == nil || !strings.Contains(err.Error(), "c2patool couldn't put Content Credentials into the video") {
		t.Fatalf("failed signing: %v", err)
	}
	if left := leftIn(t, tool); len(left) != 0 {
		t.Fatalf("a copy was left behind: %v", left)
	}
}

func TestTheRealC2patoolIsNeverRunUnderGoTest(t *testing.T) {
	cred, _ := MakeTest("", time.Now())
	tool := Tool{Tools: tools.New(config.Tools{}), Dir: t.TempDir()}
	if _, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred}); err != errRealInTest {
		t.Fatalf("sign: %v", err)
	}
	if _, err := tool.Read(context.Background(), video(t)); err != errRealInTest {
		t.Fatalf("read: %v", err)
	}
}

func TestSignerSignsOnlySignatureInputs(t *testing.T) {
	cred, _ := MakeTest("", time.Now())
	if _, err := cred.Sign([]byte("anything at all")); err != ErrNotSignature {
		t.Fatalf("signed anything: %v", err)
	}
	for _, alg := range []string{"p256", "p384", "p521", "rsa", "ed25519"} {
		c := credentialWith(t, alg)
		msg := append(append([]byte(nil), sigStructure...), "claim"...)
		sig, err := c.Sign(msg)
		if err != nil || len(sig) == 0 {
			t.Fatalf("%s: %v", alg, err)
		}
		if p := c.Problems(time.Now()); len(p) != 0 {
			t.Errorf("%s: %v", alg, p)
		}
	}
}

// c2patool runs MetaRush's signer once for --signer-info and once to sign,
// each a process of its own that inherits the socket; every run reaches the
// MetaRush that started c2patool, and exactly one claim is signed.
func TestSignerReachesMetaRushEveryTime(t *testing.T) {
	cred, err := MakeTest("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := newSession(t.TempDir(), cred, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.close()
	f := strings.Fields(sess.signer)
	run := func(stdin []byte, args ...string) ([]byte, error) {
		cmd := exec.Command(f[0], append(f[1:], args...)...)
		cmd.ExtraFiles = []*os.File{sess.peer} // as c2patool passes it on
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var errb bytes.Buffer
		cmd.Stderr = &errb
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%v: %s", err, errb.String())
		}
		return out, nil
	}
	for i := 0; i < maxInfos; i++ {
		out, err := run(nil, "--signer-info")
		var info signerInfo
		if err != nil || json.Unmarshal(out, &info) != nil || info.SignCert == "" || info.Alg != AlgES256 {
			t.Fatalf("--signer-info, run %d: %q %v", i+1, out, err)
		}
	}
	msg := append(append([]byte(nil), sigStructure...), "a claim"...)
	sig, err := run(msg)
	if err != nil || len(sig) != 64 {
		t.Fatalf("signing: %d bytes, %v", len(sig), err)
	}
	d := sha256.Sum256(msg)
	pub := cred.Leaf().PublicKey.(*ecdsa.PublicKey)
	if !ecdsa.Verify(pub, d[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature doesn't verify")
	}
	// A second claim is refused: one signing, one signature.
	if out, err := run(append(append([]byte(nil), sigStructure...), "another claim"...)); err == nil || !strings.Contains(err.Error(), "more than one claim") {
		t.Fatalf("a second claim: %q %v", out, err)
	}
	if got := sess.returned(); len(got) != 1 || !bytes.Equal(got[0], sig) {
		t.Fatalf("returned: %d", len(got))
	}
}

// The reviewer's attack (D436): while a signing runs, anything else of the
// person's reads c2patool's environment and arguments and looks for a way
// to the signer, then asks it for a signature of its own. There is none: no
// socket file, no ticket, nothing in c2patool's environment; the signer run
// from outside says so and signs nothing; the signing goes on with exactly
// its one signature.
func TestTheSignerIsNoOracle(t *testing.T) {
	cred, err := MakeTest("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	hold := filepath.Join(t.TempDir(), "hold")
	if err := os.WriteFile(hold, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tool, log := fakeTool(t, fakec2patool.State{Hold: hold})
	type result struct {
		s   *Signed
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred})
		done <- result{s, err}
	}()
	var pid int
	for deadline := time.Now().Add(time.Minute); pid == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("c2patool never got to signing")
		}
		if b, err := os.ReadFile(hold + ".ready"); err == nil {
			_, _ = fmt.Sscan(string(b), &pid)
		}
	}
	// 1. c2patool's environment and arguments, as the person's other programs see them.
	seen := processSays(t, pid)
	for _, never := range []string{"METARUSH_C2PA", "TICKET", "SOCKET", ".sock"} {
		if strings.Contains(strings.ToUpper(seen), strings.ToUpper(never)) {
			t.Errorf("c2patool's environment or arguments carry %q:\n%s", never, seen)
		}
	}
	// 2. Every socket in the signing's folder and the private folder: none to connect to.
	var sockets []string
	_ = filepath.WalkDir(tool.Dir, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
				sockets = append(sockets, p)
			}
		}
		return nil
	})
	if len(sockets) > 0 {
		t.Errorf("sockets to reach the signer: %v", sockets)
	}
	for _, sock := range sockets {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			t.Errorf("connected to %s", sock)
		}
	}
	// 3. The signer run by something else: no socket handed to it, or another one.
	exe, _ := os.Executable()
	claim := append(append([]byte(nil), sigStructure...), `{"claim":"the attacker's"}`...)
	outsider := func(extra *os.File, stdin []byte, args ...string) (string, error) {
		cmd := exec.Command(exe, append([]string{SignerCommand}, args...)...)
		if extra != nil {
			cmd.ExtraFiles = []*os.File{extra}
		}
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := outsider(nil, nil, "--signer-info"); err == nil || !strings.Contains(out, errOutsider) {
		t.Errorf("the signer run from outside answered: %q %v", out, err)
	}
	if out, err := outsider(nil, claim); err == nil || !strings.Contains(out, errOutsider) {
		t.Errorf("the signer run from outside signed: %q %v", out, err)
	}
	r, w, _ := os.Pipe()
	if out, err := outsider(r, claim); err == nil || !strings.Contains(out, errOutsider) {
		t.Errorf("the signer with a pipe signed: %q %v", out, err)
	}
	r.Close()
	w.Close()
	// Let the signing go on: it gets exactly its one signature.
	_ = os.Remove(hold)
	res := <-done
	if res.err != nil || !res.s.Verified || res.s.Signatures != 1 {
		t.Fatalf("the signing: %+v %v", res.s, res.err)
	}
	res.s.Remove()
	if calls := fakec2patool.Calls(log); len(calls) < 1 {
		t.Fatal("no calls logged")
	}
}

// processSays is a process's arguments and environment, as another program
// of the same person reads them (ps -E on a Mac, /proc elsewhere).
func processSays(t *testing.T, pid int) string {
	t.Helper()
	if runtime.GOOS == "linux" {
		env, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		args, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		return strings.ReplaceAll(string(args)+"\n"+string(env), "\x00", " ")
	}
	out, err := exec.Command("/bin/ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if !strings.Contains(string(out), "HOME=") {
		t.Fatalf("ps showed no environment: %q", out)
	}
	return string(out)
}

// What c2patool (or anything it runs) may ask for: one claim's signature.
// More is refused and the signing fails; a claim of its own first leaves its
// video unsigned by MetaRush; a signature in the copy other than the one
// MetaRush returned is refused. Nothing is left behind.
func TestOneSignaturePerSigningAndOnlyOurs(t *testing.T) {
	cred, err := MakeTest("", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		st   fakec2patool.State
		want string
	}{
		{"23 more claims", fakec2patool.State{ExtraSigns: 23}, "more than one signing needs"},
		{"a claim of its own first", fakec2patool.State{StealFirst: true}, "more than one signing needs"},
		{"another signature in the copy", fakec2patool.State{SwapSignature: true}, "signature MetaRush didn't make"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tool, _ := fakeTool(t, c.st)
			s, err := tool.Sign(context.Background(), SignIn{Video: video(t), Manifest: definition(t), Credential: cred})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("signed: %+v %v", s, err)
			}
			if left := leftIn(t, tool); len(left) != 0 {
				t.Fatalf("left behind: %v", left)
			}
		})
	}
}

// credentialWith is a brought credential: an authority and a signing
// certificate with a key of the kind asked for.
func credentialWith(t *testing.T, kind string, change ...func(*x509.Certificate)) *Credential {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Example CA", Organization: []string{"Example Authority"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(240 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	var key any
	switch kind {
	case "p384":
		key, _ = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "p521":
		key, _ = ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	case "rsa":
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
	case "ed25519":
		_, key, _ = ed25519.GenerateKey(rand.Reader)
	default:
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Example Signer", Organization: []string{"Example Studio"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(240 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	for _, f := range change {
		f(tpl)
	}
	signer, parent := any(caKey), caCert
	if tpl.Issuer.CommonName == "self" {
		signer, parent = key, tpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, publicOf(key), signer)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	c, err := Parse(chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func publicOf(k any) any {
	switch key := k.(type) {
	case *ecdsa.PrivateKey:
		return key.Public()
	case *rsa.PrivateKey:
		return key.Public()
	case ed25519.PrivateKey:
		return key.Public()
	}
	return nil
}

func TestBroughtCertificatesAreCheckedAsC2PAChecksThem(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		change func(*x509.Certificate)
		want   string
	}{
		{"self-signed", func(c *x509.Certificate) { c.Issuer.CommonName = "self"; c.Subject = pkix.Name{CommonName: "self"} }, "signed by itself"},
		{"no purpose", func(c *x509.Certificate) { c.ExtKeyUsage = nil }, "doesn't say what it is for"},
		{"any purpose", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny} }, "for anything"},
		{"server", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} }, "isn't for signing documents"},
		{"no signatures", func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageKeyEncipherment }, "digital signatures"},
		{"ran out", func(c *x509.Certificate) { c.NotBefore, c.NotAfter = now.Add(-72*time.Hour), now.Add(-24*time.Hour) }, "ran out"},
		{"an authority", func(c *x509.Certificate) { c.IsCA = true; c.KeyUsage |= x509.KeyUsageCertSign }, "certificate authority's"},
	}
	for _, c := range cases {
		cred := credentialWith(t, "p256", c.change)
		got := strings.Join(cred.Problems(now), " ")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Document signing and C2PA's own claim signing are fine.
	for _, oid := range []string{"doc", "claim"} {
		cred := credentialWith(t, "p256", func(c *x509.Certificate) {
			c.ExtKeyUsage = nil
			if oid == "doc" {
				c.UnknownExtKeyUsage = append(c.UnknownExtKeyUsage, oidDocumentSigning)
			} else {
				c.UnknownExtKeyUsage = append(c.UnknownExtKeyUsage, oidClaimSigning)
			}
		})
		if p := cred.Problems(now); len(p) > 0 {
			t.Errorf("%s: %v", oid, p)
		}
	}
}

func TestParseSaysWhatIsWrongWithTheFiles(t *testing.T) {
	good := credentialWith(t, "p256")
	der, _ := x509.MarshalPKCS8PrivateKey(good.Key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	other, _ := MakeTest("", time.Now())
	oder, _ := x509.MarshalPKCS8PrivateKey(other.Key)
	cases := []struct {
		name        string
		chain, key  []byte
		want        string
		wantNoError bool
	}{
		{"swapped", keyPEM, good.ChainPEM(), "holds a private key", false},
		{"empty chain", []byte("hello"), keyPEM, "holds no certificate MetaRush can read", false},
		{"no key", good.ChainPEM(), []byte("nothing"), "holds no private key MetaRush can read", false},
		{"protected", good.ChainPEM(), pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1}}), "protected with a password", false},
		{"another key", good.ChainPEM(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: oder}), "doesn't belong", false},
		{"issuer first", append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: good.Chain[1].Raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: good.Chain[0].Raw})...), keyPEM, "must come first", false},
	}
	for _, c := range cases {
		_, err := Parse(c.chain, c.key)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "-----") {
			t.Errorf("%s: the error quotes a file: %v", c.name, err)
		}
	}
}

func TestTestCertificatesSayWhatTheyAre(t *testing.T) {
	c, err := MakeTest("Example Studio", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := c.Facts()
	if !f.Test || f.Signer != "Example Studio" || !strings.Contains(f.Name, "test certificate") || f.Alg != AlgES256 || f.Certificates != 1 {
		t.Fatalf("facts: %+v", f)
	}
	if p := c.Problems(time.Now()); len(p) != 0 {
		t.Fatalf("a test certificate has problems: %v", p)
	}
	plain, _ := MakeTest("", time.Now())
	if pf := plain.Facts(); pf.Signer != "MetaRush test certificate" || pf.Organisation != "" {
		t.Fatalf("no organisation: %+v", pf)
	}
	if brought := credentialWith(t, "p256"); brought.Test() {
		t.Fatal("a brought certificate is a test one")
	}
}

func TestKeptInTheKeychainNeverInAFile(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	cfg, err := config.Load(home)
	if err != nil {
		cfg = config.Default(home)
	}
	c, _ := MakeTest("", time.Now())
	saved, err := Save(cfg, c, "A Person", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Kept != KeptKeychain || saved.Certificate.Fingerprint != c.Facts().Fingerprint {
		t.Fatalf("saved: %+v", saved)
	}
	st, err := os.Stat(statePath(cfg))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", st, err)
	}
	// Nothing on disk under the home holds the key.
	forms := keyForms(t, c)
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, f := range forms {
			if strings.Contains(string(b), f) {
				t.Errorf("%s holds the key", p)
			}
		}
		return nil
	})
	back, sv, err := Load(cfg)
	if err != nil || back.Facts().Fingerprint != c.Facts().Fingerprint || sv.SetBy != "A Person" {
		t.Fatalf("load: %+v %+v %v", back, sv, err)
	}
	msg := append(append([]byte(nil), sigStructure...), 1, 2, 3)
	if _, err := back.Sign(msg); err != nil {
		t.Fatal(err)
	}
	pub, err := Public(cfg)
	if err != nil || pub == nil || !pub.Certificate.Test {
		t.Fatalf("public: %+v %v", pub, err)
	}
	if had, err := Remove(cfg); err != nil || !had {
		t.Fatalf("remove: %v %v", had, err)
	}
	if _, _, err := Load(cfg); err != ErrNotSetUp {
		t.Fatalf("after remove: %v", err)
	}
	if p, _ := Public(cfg); p != nil {
		t.Fatal("public after remove")
	}
}

func TestOlder(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{{"0.27.1", "0.28.0", true}, {"0.28.3", "0.28.0", false}, {"1.0", "0.28.0", false}, {"0.28.0", "0.28.0", false}} {
		if got := older(c.v, c.than); got != c.want {
			t.Errorf("older(%s, %s) = %v", c.v, c.than, got)
		}
	}
}
