// Package fakec2patool is a stand-in `c2patool` for tests and browser runs:
// nothing is downloaded and nothing reaches the network. It answers what
// MetaRush asks (D416, D421) the way c2patool 0.28.3 does — `--version`,
// signing a video with `--manifest`, `--output`, `--signer-path` and
// `--settings`, and reading a file back as JSON — and, like the real one, it
// never holds a key: it asks the signer program it is given for the
// certificate chain (`--signer-info`) and for the signature, checks the
// signature against the certificate (c2patool verifies after signing) and
// refuses certificates c2pa-rs refuses (self-signed, no or any extended key
// usage, not for digital signatures, out of date). The copy it writes holds a
// Content Credentials box where the real one puts it (a top-level uuid box,
// purpose "manifest"), whose store is JSON instead of JUMBF/CBOR: the claim,
// the signature and the chain, so reading it back checks both the signature
// and the video's bytes.
//
// Its state file can make it fail as tests need: refuse to sign, sign with a
// key of its own instead of asking (c2patool's built-in test key), or change
// the copy's bytes after signing (a copy that doesn't check out). Every call
// is logged with its arguments and its whole environment
// (METARUSH_FAKE_C2PATOOL_LOG), so tests can look for a key where none may be.
package fakec2patool

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Version is what the fake reports unless the state says otherwise.
const Version = "0.28.3"

const (
	runEnv   = "METARUSH_FAKE_C2PATOOL"
	stateEnv = "METARUSH_FAKE_C2PATOOL_STATE"
	logEnv   = "METARUSH_FAKE_C2PATOOL_LOG"
)

// State is what the fake does.
type State struct {
	Version string `json:"version,omitempty"`
	// FailSign: signing stops with this error (c2patool's own words).
	FailSign string `json:"failSign,omitempty"`
	// OwnKey: it signs with a key of its own, never asking the signer.
	OwnKey bool `json:"ownKey,omitempty"`
	// TamperAfterSign: the copy's video bytes change after signing, so the
	// copy doesn't check out when read back.
	TamperAfterSign bool `json:"tamperAfterSign,omitempty"`
	// Log is a file that gets every call: arguments and environment (JSON lines).
	Log string `json:"log,omitempty"`
}

// Call is one logged run.
type Call struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
	// Exit is how it ended, Said what it wrote to its standard error (a
	// failing test prints them).
	Exit int    `json:"exit"`
	Said string `json:"said,omitempty"`
	// Signer is what the signer program was given (stdin is never logged:
	// it is the claim, not a secret).
	Signer []string `json:"signer,omitempty"`
}

// MaybeRun makes a test binary act as the fake when started through Write's
// wrapper. Call it in TestMain (after c2pa.MaybeSigner).
func MaybeRun() {
	if os.Getenv(runEnv) != "1" {
		return
	}
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Write puts a `c2patool` wrapper in a temp dir that runs this test binary
// as the fake (its TestMain must call MaybeRun) with st as its state, and
// returns the wrapper's path and the state file.
func Write(t testing.TB, st State) (tool, stateFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stateFile = filepath.Join(dir, "state.json")
	if err := Save(stateFile, st); err != nil {
		t.Fatal(err)
	}
	tool = filepath.Join(dir, "c2patool")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := "#!/bin/sh\nexec env " + runEnv + "=1 " + stateEnv + "=" + q(stateFile) + " " + q(exe) + " \"$@\"\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tool, stateFile
}

// Load reads a state file.
func Load(p string) State {
	var st State
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Save writes a state file.
func Save(p string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// Calls reads a log file.
func Calls(p string) []Call {
	b, _ := os.ReadFile(p)
	var out []Call
	for _, l := range bytes.Split(b, []byte("\n")) {
		var c Call
		if json.Unmarshal(l, &c) == nil && len(c.Args) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// boxUUID is Content Credentials' box in an MP4 (as c2patool writes it).
var boxUUID = []byte{0xd8, 0xfe, 0xc3, 0xd6, 0x1b, 0x0e, 0x48, 0x3c, 0x92, 0x97, 0x58, 0x28, 0x87, 0x7e, 0xc4, 0x81}

// sigPrefix starts a COSE_Sign1 signature input: what MetaRush's signer
// signs, and nothing else.
var sigPrefix = append([]byte{0x84, 0x6a}, "Signature1"...)

// store is the fake's manifest store (JSON where c2patool writes JUMBF).
type store struct {
	Label     string          `json:"label"`
	Claim     json.RawMessage `json:"claim"`
	Signature string          `json:"signature"`
	Alg       string          `json:"alg"`
	Chain     string          `json:"chain"`
	TSA       string          `json:"tsa,omitempty"`
}

// claim is what is signed: the manifest definition and the video's hash.
type claim struct {
	Manifest json.RawMessage `json:"manifest"`
	VideoSHA string          `json:"videoSha256"`
	Label    string          `json:"label"`
}

// Main runs the fake with args and returns the exit code; with a log, each
// run is logged once it ends.
func Main(args []string, stdout, stderr io.Writer) int {
	st := Load(os.Getenv(stateEnv))
	p := firstNonEmpty(os.Getenv(logEnv), st.Log)
	if p == "" {
		return run(st, args, stdout, stderr)
	}
	var said bytes.Buffer
	code := run(st, args, stdout, io.MultiWriter(stderr, &said))
	logCall(p, Call{Args: args, Env: os.Environ(), Exit: code, Said: said.String()})
	return code
}

func run(st State, args []string, stdout, stderr io.Writer) int {
	var path, manifest, output, signer, settings string
	force := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a {
		case "-V", "--version":
			fmt.Fprintf(stdout, "c2patool %s\n", firstNonEmpty(st.Version, Version))
			return 0
		case "-m", "--manifest":
			manifest = val()
		case "-o", "--output":
			output = val()
		case "--signer-path":
			signer = val()
		case "--settings":
			settings = val()
		case "-f", "--force":
			force = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(stderr, "error: unexpected argument '%s' found\n", a)
				return 2
			}
			path = a
		}
	}
	if path == "" {
		fmt.Fprintln(stderr, "error: the following required arguments were not provided: <PATH>")
		return 2
	}
	if settings != "" {
		if b, err := os.ReadFile(settings); err != nil || !json.Valid(b) {
			fmt.Fprintln(stderr, "Error: Could not configure c2pa-rs")
			return 1
		}
	}
	if manifest == "" {
		return read(path, stdout, stderr)
	}
	return sign(st, path, manifest, output, signer, force, stdout, stderr)
}

func logCall(p string, c Call) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(c)
	_, _ = f.Write(append(b, '\n'))
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func fail(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "Error: embedding manifest\n\nCaused by:\n    %s\n", why)
	return 1
}

func sign(st State, path, manifest, output, signer string, force bool, stdout, stderr io.Writer) int {
	if output == "" {
		fmt.Fprintln(stderr, "Error: The -o/--output argument is required for this operation")
		return 1
	}
	if !strings.EqualFold(filepath.Ext(output), filepath.Ext(path)) {
		fmt.Fprintln(stderr, "Error: Output type must match source type")
		return 1
	}
	if _, err := os.Lstat(output); err == nil && !force {
		fmt.Fprintln(stderr, "Error: Output already exists; use -f/force to force write")
		return 1
	}
	def, err := os.ReadFile(manifest)
	if err != nil || !json.Valid(def) {
		fmt.Fprintln(stderr, "Error: reading manifest configuration")
		return 1
	}
	var probe struct {
		PrivateKey string `json:"private_key"`
		SignCert   string `json:"sign_cert"`
	}
	_ = json.Unmarshal(def, &probe)
	video, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "Error: File not found: %s\n", path)
		return 1
	}
	if st.FailSign != "" {
		return fail(stderr, st.FailSign)
	}
	label := "urn:c2pa:" + randomHex(16)
	sum := sha256.Sum256(video)
	cl, _ := json.Marshal(claim{Manifest: def, VideoSHA: hex.EncodeToString(sum[:]), Label: label})
	toSign := append(append([]byte(nil), sigPrefix...), cl...)
	var alg, chainPEM, tsa string
	var sig []byte
	switch {
	case st.OwnKey || signer == "":
		// c2patool's built-in test certificate: what MetaRush must never accept.
		key, cert := ownKey()
		alg, chainPEM = "es256", cert
		d := sha256.Sum256(toSign)
		r, s, _ := ecdsa.Sign(rand.Reader, key, d[:])
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		fmt.Fprintln(stderr, "Note: Using default private key and signing certificate. This is only valid for development.")
	default:
		bin, base := splitCommand(signer)
		var info struct {
			Alg      string `json:"alg"`
			SignCert string `json:"sign_cert"`
			TSAURL   string `json:"tsa_url"`
		}
		out, err := runSigner(bin, append(base, "--signer-info"), nil)
		if err != nil {
			fmt.Fprintf(stderr, "Error: Subprocess --signer-info failed for %q: %v\n", bin, err)
			return 1
		}
		if json.Unmarshal(out, &info) != nil || info.SignCert == "" {
			fmt.Fprintf(stderr, "Error: Parsing --signer-info JSON from %q\n", bin)
			return 1
		}
		alg, chainPEM, tsa = strings.ToLower(info.Alg), info.SignCert, info.TSAURL
		if why := certProblem(chainPEM); why != "" {
			return fail(stderr, why)
		}
		sig, err = runSigner(bin, base, toSign)
		if err != nil {
			return fail(stderr, "User supplied signer process failed. Its stderr output was: \n"+err.Error())
		}
		if len(sig) == 0 {
			return fail(stderr, "User supplied process succeeded, but the external process did not write signature bytes to stdout")
		}
	}
	if !verify(alg, chainPEM, toSign, sig) {
		return fail(stderr, "manifest failed validation with: failures:\n      claimSignature.mismatch: claim signature is not valid")
	}
	s := store{Label: label, Claim: cl, Signature: base64.StdEncoding.EncodeToString(sig), Alg: alg, Chain: chainPEM, TSA: tsa}
	body, _ := json.Marshal(s)
	jumb := make([]byte, 8)
	binary.BigEndian.PutUint32(jumb, uint32(8+len(body)))
	copy(jumb[4:], "jumb")
	payload := append(append([]byte{0, 0, 0, 0}, "manifest\x00"...), make([]byte, 8)...)
	payload = append(payload, jumb...)
	payload = append(payload, body...)
	box := make([]byte, 8, 8+16+len(payload))
	binary.BigEndian.PutUint32(box, uint32(8+16+len(payload)))
	copy(box[4:], "uuid")
	box = append(box, boxUUID...)
	box = append(box, payload...)
	out := append(box, video...)
	if st.TamperAfterSign && len(video) > 0 {
		out[len(out)-1] ^= 0xff
	}
	if err := os.WriteFile(output, out, 0o644); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	rep := report(s, out)
	b, _ := json.MarshalIndent(rep, "", "  ")
	_, _ = stdout.Write(append(b, '\n'))
	return 0
}

// splitCommand splits a signer command at spaces, as c2patool does (paths
// with spaces aren't supported).
func splitCommand(cmd string) (string, []string) {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return "", nil
	}
	return f[0], f[1:]
}

func runSigner(bin string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, runEnv+"=") && !strings.HasPrefix(e, stateEnv+"=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// certProblem refuses what c2pa-rs refuses (c2patool 0.28.3's words).
func certProblem(chainPEM string) string {
	blk, _ := pem.Decode([]byte(chainPEM))
	if blk == nil {
		return "the certificate is invalid"
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "the certificate is invalid"
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return "the certificate was not valid at time of signing"
	}
	if bytes.Equal(leaf.RawIssuer, leaf.RawSubject) || leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return "the certificate is invalid"
	}
	ok := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageAny {
			return "the certificate is invalid"
		}
		ok = ok || u == x509.ExtKeyUsageEmailProtection
	}
	for _, u := range leaf.UnknownExtKeyUsage {
		ok = ok || u.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 36}) || u.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 62558, 2, 1})
	}
	if !ok {
		return "the certificate is invalid"
	}
	return ""
}

func leafOf(chainPEM string) *x509.Certificate {
	blk, _ := pem.Decode([]byte(chainPEM))
	if blk == nil {
		return nil
	}
	x, _ := x509.ParseCertificate(blk.Bytes)
	return x
}

// verify checks a COSE signature as c2pa-rs does after signing.
func verify(alg, chainPEM string, msg, sig []byte) bool {
	leaf := leafOf(chainPEM)
	if leaf == nil {
		return false
	}
	switch pub := leaf.PublicKey.(type) {
	case *ecdsa.PublicKey:
		n := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) != 2*n {
			return false
		}
		var d []byte
		switch alg {
		case "es384":
			s := sha512.Sum384(msg)
			d = s[:]
		case "es512":
			s := sha512.Sum512(msg)
			d = s[:]
		default:
			s := sha256.Sum256(msg)
			d = s[:]
		}
		return ecdsa.Verify(pub, d, new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:]))
	case *rsa.PublicKey:
		d := sha256.Sum256(msg)
		return rsa.VerifyPSS(pub, crypto.SHA256, d[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(pub, msg, sig)
	}
	return false
}

// read prints a file's Content Credentials as c2patool does, or "No claim found".
func read(path string, stdout, stderr io.Writer) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "Error: File not found: %s\n", path)
		return 1
	}
	s, rest, ok := findStore(b)
	if !ok {
		fmt.Fprintln(stderr, "Error: No claim found")
		return 1
	}
	_ = rest
	rep := report(s, b)
	out, _ := json.MarshalIndent(rep, "", "  ")
	_, _ = stdout.Write(append(out, '\n'))
	return 0
}

// findStore finds the fake's Content Credentials box at the top level and
// returns its store and the file without it.
func findStore(b []byte) (store, []byte, bool) {
	var off int
	for off+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[off : off+4]))
		if size < 8 || off+size > len(b) {
			break
		}
		if string(b[off+4:off+8]) == "uuid" && size >= 24 && bytes.Equal(b[off+8:off+24], boxUUID) {
			body := b[off+24 : off+size]
			if len(body) < 4+9+8+8 {
				break
			}
			body = body[4+9+8:] // version/flags, "manifest\0", offset
			jumbLen := int(binary.BigEndian.Uint32(body[:4]))
			if jumbLen < 8 || jumbLen > len(body) {
				break
			}
			var s store
			if json.Unmarshal(body[8:jumbLen], &s) != nil {
				break
			}
			rest := append(append([]byte(nil), b[:off]...), b[off+size:]...)
			return s, rest, true
		}
		off += size
	}
	return store{}, nil, false
}

// report is c2patool's JSON report of a store in a file.
func report(s store, file []byte) map[string]any {
	var cl claim
	_ = json.Unmarshal(s.Claim, &cl)
	var def map[string]any
	_ = json.Unmarshal(cl.Manifest, &def)
	sig, _ := base64.StdEncoding.DecodeString(s.Signature)
	toSign := append(append([]byte(nil), sigPrefix...), s.Claim...)
	var status []map[string]any
	add := func(code, why string) {
		status = append(status, map[string]any{"code": code, "url": "self#jumbf=/c2pa/" + s.Label + "/c2pa.signature", "explanation": why})
	}
	add("signingCredential.untrusted", "signing certificate untrusted")
	invalid := false
	if !verify(s.Alg, s.Chain, toSign, sig) {
		add("claimSignature.mismatch", "claim signature is not valid")
		invalid = true
	}
	_, rest, _ := findStore(file)
	sum := sha256.Sum256(rest)
	if hex.EncodeToString(sum[:]) != cl.VideoSHA {
		add("assertion.bmffHash.mismatch", "asset hash error, name: jumbf manifest, error: hash verification( BMFF file level hash mismatch )")
		invalid = true
	}
	state := "Valid"
	if invalid {
		state = "Invalid"
	}
	gen, _ := def["claim_generator_info"].([]any)
	if len(gen) > 0 {
		if g, ok := gen[0].(map[string]any); ok {
			g["org.contentauth.c2pa_rs"] = "0.91.6"
		}
	}
	var assertions []any
	assertions = append(assertions, map[string]any{"label": "c2pa.hash.bmff.v3", "data": map[string]any{"alg": "sha256", "name": "jumbf manifest"}, "created": true})
	if as, ok := def["assertions"].([]any); ok {
		for _, a := range as {
			if m, ok := a.(map[string]any); ok {
				if l, _ := m["label"].(string); l == "c2pa.actions" {
					m["label"] = "c2pa.actions.v2"
				}
				assertions = append(assertions, m)
			}
		}
	}
	info := map[string]any{"alg": capital(s.Alg)} // c2patool says "Es256"
	if leaf := leafOf(s.Chain); leaf != nil {
		// c2patool's "issuer" is who issued the claim: the signing
		// certificate's own organisation (empty without one).
		info["issuer"] = orgOf(leaf.Subject)
		info["common_name"] = leaf.Subject.CommonName
		info["cert_serial_number"] = leaf.SerialNumber.String()
	}
	if s.TSA != "" {
		info["time"] = time.Now().UTC().Format(time.RFC3339)
	}
	m := map[string]any{"claim_generator_info": def["claim_generator_info"], "title": def["title"], "instance_id": "xmp.iid:" + randomHex(8),
		"ingredients": def["ingredients"], "assertions": assertions, "signature_info": info, "label": s.Label, "claim_version": 2}
	return map[string]any{"active_manifest": s.Label, "manifests": map[string]any{s.Label: m}, "validation_status": status, "validation_state": state}
}

func orgOf(n pkix.Name) string {
	if len(n.Organization) > 0 {
		return n.Organization[0]
	}
	return ""
}

// ownKey is the fake's own key and self-issued certificate (c2patool's
// built-in test certificate stands for this).
func ownKey() (*ecdsa.PrivateKey, string) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "C2PA Test Signing Cert", Organization: []string{"C2PA Test"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
