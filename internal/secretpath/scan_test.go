package secretpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/secret-scan.sh fails on planted secrets in tracked files and passes
// once they are allowlisted. Fake values are assembled at run time so this
// file itself never matches the scanner.
func TestSecretScanScript(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	script, err := filepath.Abs("../../scripts/secret-scan.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skip("scripts/secret-scan.sh not present")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", rel)
	}
	scan := func() (string, int) {
		cmd := exec.Command("bash", script, repo)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	write("README.md", "nothing to see\n")
	if out, code := scan(); code != 0 {
		t.Fatalf("clean repo flagged (%d): %s", code, out)
	}
	planted := map[string]string{
		"conf/db.txt":    "uri: mongodb" + "+srv://admin:" + "hunter2@prod.cluster.net/app\n",
		"conf/aws.ini":   "aws_access_key_id = AKIA" + "ABCDEFGHIJKLMNOP\n",
		"conf/tts.env":   "KEY=sk_" + strings.Repeat("a1b2", 12) + "\n",
		"conf/tigris.sh": "export S=tsec_" + strings.Repeat("Zx9", 10) + "\n",
		"conf/key.pem":   "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIE\n",
		// A Google (Gemini) API key: AIza and 35 more characters.
		"conf/gemini.env": "METARUSH_GEMINI_API_KEY=" + "AI" + "za" + strings.Repeat("Qw3_", 8) + "Zz9\n",
	}
	for rel, body := range planted {
		write(rel, body)
	}
	out, code := scan()
	if code != 1 {
		t.Fatalf("planted secrets not caught (exit %d): %s", code, out)
	}
	for rel := range planted {
		if !strings.Contains(out, rel) {
			t.Errorf("%s not reported:\n%s", rel, out)
		}
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, strings.Repeat("a1b2", 12)) {
		t.Fatalf("the scanner must not echo secrets:\n%s", out)
	}
	// Allowlist entries and the inline marker clear findings.
	for rel := range planted {
		run("rm", "-q", "--cached", rel)
		_ = os.Remove(filepath.Join(repo, rel))
	}
	write("docs/example.md", "mongodb"+"+srv://user:"+"pass@cluster0.example.mongodb.net\n")
	write("x_test.go", "k := \"sk_"+strings.Repeat("f", 30)+"\" // secret-scan: allow\n")
	write("scripts/secret-scan.allow", "example\\.mongodb\\.net\n")
	if out, code := scan(); code != 0 {
		t.Fatalf("allowlisted placeholders flagged (%d): %s", code, out)
	}
}
