package abilities

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The starter pack (abilities/ at the repository's root, D283–D287): each
// ability is checked from its folder as shipped, approved, set up and run
// against a fake of its service on this computer. Nothing here reaches the
// real services: the proxy's dial seam sends every request for the service's
// names to a local test server that plays it.

// starterFolder is a starter ability's folder in this repository.
func starterFolder(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "abilities", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p, ManifestFile)); err != nil {
		t.Fatalf("the starter ability %s: %v", name, err)
	}
	return p
}

// fakeService records what a fake service was asked.
type fakeService struct {
	mu   sync.Mutex
	seen []*http.Request
}

func (f *fakeService) saw(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, r.Clone(context.Background()))
}

// asked lists the requests one host got.
func (f *fakeService) asked(host string) []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*http.Request
	for _, r := range f.seen {
		if strings.EqualFold(r.Host, host) {
			out = append(out, r)
		}
	}
	return out
}

// fakeHosts serves h over HTTPS under the given names (a test certificate
// authority the run's proxy trusts) and, for plain HTTP, under host:port
// names given as "http://host:port": the proxy dials them here instead of
// the internet. Anything else is "no such host".
func fakeHosts(t *testing.T, h http.Handler, names ...string) *fakeService {
	t.Helper()
	f := &fakeService{}
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.saw(r)
		h.ServeHTTP(w, r)
	})
	var tlsNames []string
	plain := map[string]bool{}
	for _, n := range names {
		if strings.HasPrefix(n, "http://") {
			plain[strings.TrimPrefix(n, "http://")] = true
		} else {
			tlsNames = append(tlsNames, n)
		}
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	var tlsAddr string
	if len(tlsNames) > 0 {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: tlsNames[0]}, DNSNames: tlsNames, NotBefore: time.Now().Add(-time.Hour),
			NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, _ := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
		srv := httptest.NewUnstartedServer(record)
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
		srv.StartTLS()
		t.Cleanup(srv.Close)
		tlsAddr = srv.Listener.Addr().String()
	}
	var plainAddr string
	if len(plain) > 0 {
		srv := httptest.NewServer(record)
		t.Cleanup(srv.Close)
		plainAddr = srv.Listener.Addr().String()
	}
	known := map[string]bool{}
	for _, n := range tlsNames {
		known[n] = true
	}
	testDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		if plain[addr] {
			return d.DialContext(ctx, network, plainAddr)
		}
		host, port, _ := net.SplitHostPort(addr)
		if known[host] && port == "443" {
			return d.DialContext(ctx, network, tlsAddr)
		}
		return nil, &net.DNSError{Err: "no such host (test)", Name: host, IsNotFound: true}
	}
	testRootCAs = pool
	t.Cleanup(func() { testDial, testRootCAs = nil, nil })
	return f
}

// starterApp is a test app for the starter pack: its keys in its own home,
// none of this process's left behind.
func starterApp(t *testing.T, keys ...string) *app.App {
	t.Helper()
	a := newApp(t)
	for _, k := range keys {
		t.Setenv("METARUSH_"+strings.ToUpper(strings.ReplaceAll(k, "-", "_"))+"_API_KEY", "")
	}
	needSandbox(t, a)
	return a
}

// approveStarter checks a starter ability's folder as shipped (its test runs
// once in the sandbox, network off), approves it and sets it up with keys.
func approveStarter(t *testing.T, a *app.App, name string, keys map[string]string) *AbilityCheckOut {
	t.Helper()
	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: starterFolder(t, name)}, ops.Caller{})
	if !out.OK {
		log := ""
		if out.Test != nil {
			log = out.Test.Log
		}
		t.Fatalf("the check of %s: %v\n%s", name, problemTexts(out.Problems), log)
	}
	human := ops.Caller{Human: true}
	mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: out.Name + "@" + out.Version}, human)
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: out.Name, Keys: keys}, human)
	return out
}

// runStarter plans and runs a starter ability as a person, plays the worker
// and returns the finished run with its requests and log.
func runStarter(t *testing.T, a *app.App, name string, inputs map[string]any) *AbilityRunsOut {
	t.Helper()
	in := AbilityPlanIn{Ref: name, Inputs: inputs}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if !p.Ready {
		t.Fatalf("the plan of %s: %v", name, p.Problems)
	}
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint, MaxUSD: p.Estimate.USD}, ops.Caller{Human: true})
	if _, err := runJob(t, a, run.JobID); err != nil {
		out := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{})
		t.Fatalf("the run of %s: %v\n%s\n%+v", name, err, out.Log, out.Requests)
	}
	return mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{})
}

// madeUpKey is a key for a fake service, built at run time.
func madeUpKey(service string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x", service, b)
}

// noKeyIn fails when a run's record holds the key.
func noKeyIn(t *testing.T, out *AbilityRunsOut, key string) {
	t.Helper()
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), key) || strings.Contains(out.Log, key) {
		t.Fatalf("the run's record holds the key:\n%s", out.Log)
	}
}

// keyedHosts are the hosts a run's requests carried a key to, by key name.
func keyedHosts(out *AbilityRunsOut) map[string][]string {
	m := map[string][]string{}
	for _, q := range out.Requests {
		for _, k := range q.Keys {
			if !contains(m[k], q.Host) {
				m[k] = append(m[k], q.Host)
			}
		}
	}
	return m
}

// seedOf turns a path into a small number (different paths, different files).
func seedOf(s string) int {
	n := 0
	for _, c := range s {
		n = n*31 + int(c)
	}
	if n < 0 {
		n = -n
	}
	return n % 6
}

// testJPEG is a small JPEG whose pixels depend on seed (different files).
func testJPEG(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for x := 0; x < 64; x++ {
		for y := 0; y < 36; y++ {
			img.Set(x, y, color.RGBA{uint8(seed * 40), uint8(x * 4), uint8(y * 7), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// testMedia makes a short file with ffmpeg (a clip, a sound), or skips.
func testMedia(t *testing.T, ext string, args ...string) []byte {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on this computer")
	}
	out := filepath.Join(t.TempDir(), "media"+ext)
	cmd := exec.Command(ff, append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testMP4(t *testing.T) []byte {
	return testMedia(t, ".mp4", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "mpeg4", "-q:v", "6", "-c:a", "aac", "-shortest")
}

// assetOf and resourceOf read what a run brought in.
func assetOf(t *testing.T, a *app.App, id string) models.Asset {
	t.Helper()
	var as models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		t.Fatal(err)
	}
	return as
}

func resourceOf(t *testing.T, a *app.App, id string) models.Resource {
	t.Helper()
	var r models.Resource
	if err := a.DB.GetByID(context.Background(), store.CResources, a.WorkspaceID(), id, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- Pexels ----

func pexelsFake(t *testing.T, key string, mp4 []byte) *fakeService {
	return fakeHosts(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "api.pexels.com":
			if r.Header.Get("Authorization") != key {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]string{"error": "bad key"})
				return
			}
			q := r.URL.Query().Get("query")
			switch r.URL.Path {
			case "/v1/search":
				var photos []map[string]any
				for i := 1; i <= 2; i++ {
					photos = append(photos, map[string]any{"id": 3000 + i, "url": fmt.Sprintf("https://www.pexels.com/photo/%s-%d/", strings.ReplaceAll(q, " ", "-"), 3000+i),
						"photographer": fmt.Sprintf("Photographer %d", i), "alt": fmt.Sprintf("%s, picture %d", q, i),
						"src": map[string]string{"large2x": fmt.Sprintf("https://images.pexels.com/photos/%d/pexels-photo-%d.jpeg?w=1880", 3000+i, 3000+i)}})
				}
				writeJSON(w, map[string]any{"photos": photos})
			case "/v1/videos/search":
				writeJSON(w, map[string]any{"videos": []map[string]any{{"id": 4001, "url": "https://www.pexels.com/video/4001/", "user": map[string]string{"name": "Video Maker"},
					"video_files": []map[string]any{
						{"quality": "hd", "file_type": "video/mp4", "width": 1920, "link": "https://player.vimeo.com/external/4001.hd.mp4"},
						{"quality": "uhd", "file_type": "video/mp4", "width": 3840, "link": "https://videos.pexels.com/video-files/4001/uhd.mp4"},
						{"quality": "hd", "file_type": "video/mp4", "width": 1920, "link": "https://videos.pexels.com/video-files/4001/hd.mp4"}}}}})
			default:
				http.NotFound(w, r)
			}
		case "images.pexels.com":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(testJPEG(seedOf(r.URL.Path)))
		case "videos.pexels.com":
			if !strings.HasSuffix(r.URL.Path, "/hd.mp4") {
				http.Error(w, "the script should pick the HD file", http.StatusTeapot)
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(mp4)
		default:
			http.NotFound(w, r)
		}
	}), "api.pexels.com", "images.pexels.com", "videos.pexels.com")
}

func TestStarterPexels(t *testing.T) {
	a := starterApp(t, "pexels")
	key := madeUpKey("pexels")
	mp4 := testMP4(t)
	svc := pexelsFake(t, key, mp4)
	chk := approveStarter(t, a, "pexels-stock", map[string]string{"pexels": key})
	if !strings.Contains(chk.Test.Log, "would bring in pexels-video-1001.mp4") {
		t.Fatalf("the offline check reads the saved answer:\n%s", chk.Test.Log)
	}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: "pexels-stock", Inputs: map[string]any{"words": "ocean"}}, ops.Caller{})
	if p.Decides != DecideKeyed || !strings.Contains(p.Leaves, "The words “ocean” may leave this computer, only for api.pexels.com") {
		t.Fatalf("the plan: %+v", p)
	}

	// Photos: pictures with the photographer's credit.
	out := runStarter(t, a, "pexels-stock", map[string]any{"words": "ocean", "kind": "photos", "count": 2})
	noKeyIn(t, out, key)
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 2 {
		t.Fatalf("the photos run: %+v\n%s", out.Run, out.Log)
	}
	for i, o := range out.Run.Outputs {
		if o.Status != "added" || o.ResourceID == "" || o.Licence != "Pexels License" {
			t.Fatalf("photo %d: %+v", i, o)
		}
		r := resourceOf(t, a, o.ResourceID)
		src := r.Latest().Source
		if src.Author == "" || src.License != "Pexels License" || src.MadeWith == nil || src.MadeWith.Ability == nil ||
			!strings.HasPrefix(src.MadeWith.Ability.Credit, "Photo by Photographer ") || !strings.HasPrefix(src.URL, "https://www.pexels.com/photo/") {
			t.Fatalf("photo %d's source: %+v %+v", i, src, src.MadeWith)
		}
	}
	// The key went only to the API, never to where the pictures are.
	if k := keyedHosts(out); len(k["pexels"]) != 1 || k["pexels"][0] != "api.pexels.com" {
		t.Fatalf("where the key went: %v", k)
	}
	for _, r := range svc.asked("images.pexels.com") {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("the pictures' host got an Authorization header")
		}
	}

	// A video: footage whose rights say its licence and credit (D38).
	out = runStarter(t, a, "pexels-stock", map[string]any{"words": "ocean", "count": 1})
	if len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Status != "added" || out.Run.Outputs[0].AssetID == "" {
		t.Fatalf("the video run: %+v\n%s", out.Run, out.Log)
	}
	as := assetOf(t, a, out.Run.Outputs[0].AssetID)
	if as.Rights == nil || as.Rights.License != "Pexels License" || as.Rights.Attribution != "Video by Video Maker on Pexels" || as.Rights.Source != "https://www.pexels.com/video/4001/" {
		t.Fatalf("the clip's rights: %+v", as.Rights)
	}
	if as.Source == nil || as.Source.Uploader != "Video Maker" || !strings.Contains(as.Source.Credit(), "Video by Video Maker on Pexels (Pexels License): https://www.pexels.com/video/4001/") {
		t.Fatalf("the clip's source and credit: %+v %q", as.Source, as.Source.Credit())
	}

	// A key Pexels refuses: the run says what to do.
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "pexels-stock", Keys: map[string]string{"pexels": madeUpKey("wrong")}}, ops.Caller{Human: true})
	in := AbilityPlanIn{Ref: "pexels-stock", Inputs: map[string]any{"words": "ocean"}}
	p = mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, ops.Caller{Human: true})
	if _, err := runJob(t, a, run.JobID); err == nil {
		t.Fatal("a refused key fails the run")
	}
	if bad := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{}); !strings.Contains(bad.Log, "Pexels didn't accept the key") {
		t.Fatalf("the refused key's log:\n%s", bad.Log)
	}
}

// ---- Wikimedia Commons and the Internet Archive ----

func openMediaFake(t *testing.T, wav []byte) *fakeService {
	return fakeHosts(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "MetaRush-open-media/1.0 (") {
			http.Error(w, "say who you are", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		switch {
		case r.Host == "commons.wikimedia.org" && r.URL.Path == "/w/api.php":
			if !strings.Contains(q.Get("gsrsearch"), "filetype:bitmap") {
				writeJSON(w, map[string]any{"query": map[string]any{"pages": []any{}}})
				return
			}
			meta := func(short, code, artist string) map[string]any {
				return map[string]any{"LicenseShortName": map[string]string{"value": short}, "License": map[string]string{"value": code}, "Artist": map[string]string{"value": artist}}
			}
			writeJSON(w, map[string]any{"query": map[string]any{"pages": []any{
				map[string]any{"title": "File:Harbour light.jpg", "index": 1, "imageinfo": []any{map[string]any{"mime": "image/jpeg", "width": 800, "size": 1000,
					"url": "https://upload.wikimedia.org/wikipedia/commons/1/11/Harbour_light.jpg", "descriptionurl": "https://commons.wikimedia.org/wiki/File:Harbour_light.jpg",
					"extmetadata": meta("CC BY 4.0", "cc-by-4.0", `<a href="//commons.wikimedia.org/wiki/User:Sea_Painter">Sea Painter</a>`)}}},
				map[string]any{"title": "File:Harbour brand.png", "index": 2, "imageinfo": []any{map[string]any{"mime": "image/png", "width": 300,
					"url": "https://upload.wikimedia.org/wikipedia/commons/2/22/Harbour_brand.png", "extmetadata": meta("CC BY-NC 2.0", "cc-by-nc-2.0", "Someone")}}},
			}}})
		case r.Host == "upload.wikimedia.org":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(testJPEG(seedOf(r.URL.Path) + 1))
		case r.Host == "archive.org" && r.URL.Path == "/advancedsearch.php":
			if !strings.Contains(q.Get("q"), "licenseurl:") {
				http.Error(w, "the search should ask for a licence", http.StatusBadRequest)
				return
			}
			mt := map[bool]string{true: "audio", false: "image"}[strings.Contains(q.Get("q"), "mediatype:(audio)")]
			writeJSON(w, map[string]any{"response": map[string]any{"docs": []any{
				map[string]any{"identifier": "test-broken-item", "title": "An item the Archive can't describe", "licenseurl": "http://creativecommons.org/publicdomain/zero/1.0/"},
				map[string]any{"identifier": "test-" + mt + "-item", "title": "Old harbour " + mt, "creator": []string{"Harbour Society"}, "licenseurl": "http://creativecommons.org/publicdomain/zero/1.0/"}}}})
		case r.Host == "archive.org" && r.URL.Path == "/metadata/test-broken-item":
			http.Error(w, "busy", http.StatusServiceUnavailable)
		case r.Host == "archive.org" && strings.HasPrefix(r.URL.Path, "/metadata/"):
			id := strings.TrimPrefix(r.URL.Path, "/metadata/")
			file := map[string]any{"name": "harbour.jpg", "source": "original", "format": "JPEG", "size": "4000"}
			if strings.Contains(id, "audio") {
				file = map[string]any{"name": "harbour bells.wav", "source": "original", "format": "WAVE", "size": fmt.Sprint(len(wav))}
			}
			writeJSON(w, map[string]any{"files": []any{file, map[string]any{"name": "__ia_thumb.jpg", "format": "Item Tile", "size": "100"}}, "metadata": map[string]any{"identifier": id}})
		case r.Host == "archive.org" && strings.HasPrefix(r.URL.Path, "/download/"):
			// The Internet Archive sends downloads on to the server holding the item.
			http.Redirect(w, r, "https://ia800001.us.archive.org/1/items/"+strings.TrimPrefix(r.URL.EscapedPath(), "/download/"), http.StatusFound)
		case r.Host == "ia800001.us.archive.org":
			if strings.HasSuffix(r.URL.Path, ".wav") {
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(wav)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(testJPEG(seedOf(r.URL.Path) + 3))
		default:
			http.NotFound(w, r)
		}
	}), "commons.wikimedia.org", "upload.wikimedia.org", "archive.org", "ia800001.us.archive.org")
}

func TestStarterOpenMedia(t *testing.T) {
	a := starterApp(t)
	wav := testMedia(t, ".wav", "-f", "lavfi", "-i", "sine=frequency=660:duration=1", "-c:a", "pcm_s16le")
	openMediaFake(t, wav)
	chk := approveStarter(t, a, "open-media", nil)
	if !strings.Contains(chk.Test.Log, "left out “Lighthouse logo”: its licence (Fair use)") || !strings.Contains(chk.Test.Log, "would bring in Lighthouse postcards") {
		t.Fatalf("the offline check filters licences on the saved answers:\n%s", chk.Test.Log)
	}
	// Free, no key, only words leave: anyone starts it, Claude in a terminal too.
	in := AbilityPlanIn{Ref: "open-media", Inputs: map[string]any{"words": "harbour", "count": 2, "contact": "editor@example.com"}}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if p.Decides != DecideSafe || !p.Ready {
		t.Fatalf("the plan: %+v", p)
	}
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, ops.Caller{})
	if _, err := runJob(t, a, run.JobID); err != nil {
		t.Fatal(err)
	}
	out := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{})
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 2 || !strings.Contains(out.Log, "left out “Harbour brand”: its licence (CC BY-NC 2.0)") ||
		!strings.Contains(out.Log, "left out “An item the Archive can't describe”: the Internet Archive didn't describe it (503)") {
		t.Fatalf("the pictures run: %+v\n%s", out.Run, out.Log)
	}
	credits := map[string]string{}
	for _, o := range out.Run.Outputs {
		if o.Status != "added" || o.ResourceID == "" {
			t.Fatalf("an output: %+v", o)
		}
		res := resourceOf(t, a, o.ResourceID)
		src := res.Latest().Source
		credits[src.License] = src.MadeWith.Ability.Credit
	}
	if credits["CC BY 4.0"] != "“Harbour light” by Sea Painter, CC BY 4.0, via Wikimedia Commons" ||
		credits["CC0 1.0"] != "“Old harbour image” by Harbour Society, CC0 1.0, via the Internet Archive" {
		t.Fatalf("the credits: %v", credits)
	}
	// The download went on to the server holding the item (*.archive.org).
	seen := map[string]bool{}
	for _, q := range out.Requests {
		seen[q.Host] = q.Refused == ""
	}
	if !seen["ia800001.us.archive.org"] || !seen["upload.wikimedia.org"] {
		t.Fatalf("the requests: %+v", out.Requests)
	}

	// Sound from the Internet Archive: a clip whose rights say its licence and credit.
	out = runStarter(t, a, "open-media", map[string]any{"words": "harbour bells", "kind": "sound", "from": "internet-archive", "count": 1})
	if len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Kind != OutSound || out.Run.Outputs[0].AssetID == "" {
		t.Fatalf("the sound run: %+v\n%s", out.Run, out.Log)
	}
	as := assetOf(t, a, out.Run.Outputs[0].AssetID)
	if as.Rights == nil || as.Rights.License != "CC0 1.0" || as.Rights.Attribution != "“Old harbour audio” by Harbour Society, CC0 1.0, via the Internet Archive" ||
		as.Rights.Source != "https://archive.org/details/test-audio-item" {
		t.Fatalf("the sound's rights: %+v", as.Rights)
	}
	// Bytes already in the library keep the rights they have, or don't have:
	// a run never writes its licence onto a clip it didn't bring in.
	if err := a.DB.Update(context.Background(), store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$unset": bson.M{"rights": ""}}); err != nil {
		t.Fatal(err)
	}
	out = runStarter(t, a, "open-media", map[string]any{"words": "harbour bells", "kind": "sound", "from": "internet-archive", "count": 1})
	if len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Status != "already there" || assetOf(t, a, as.ID).Rights != nil {
		t.Fatalf("the same sound again: %+v, rights %+v", out.Run.Outputs, assetOf(t, a, as.ID).Rights)
	}
}

// ---- Freesound ----

func TestStarterFreesound(t *testing.T) {
	a := starterApp(t, "freesound")
	key := madeUpKey("freesound")
	mp3 := testMedia(t, ".mp3", "-f", "lavfi", "-i", "sine=frequency=880:duration=1", "-c:a", "libmp3lame", "-b:a", "128k")
	svc := fakeHosts(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "freesound.org" && r.URL.Path == "/apiv2/search/text/":
			if r.Header.Get("Authorization") != "Token "+key {
				w.WriteHeader(http.StatusUnauthorized)
				writeJSON(w, map[string]string{"detail": "Invalid token."})
				return
			}
			if f := r.URL.Query().Get("filter"); !strings.Contains(f, `license:("Creative Commons 0" OR "Attribution")`) || !strings.Contains(f, "duration:[0 TO 20]") {
				http.Error(w, "unexpected filter "+f, http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"count": 3, "results": []any{
				map[string]any{"id": 81, "name": "Rain on a tin roof.wav", "username": "rain_maker", "license": "https://creativecommons.org/licenses/by/4.0/",
					"url": "https://freesound.org/people/rain_maker/sounds/81/", "previews": map[string]string{"preview-hq-mp3": "https://cdn.freesound.org/previews/0/81_1-hq.mp3"}},
				map[string]any{"id": 82, "name": "Rain (NC)", "username": "other", "license": "https://creativecommons.org/licenses/by-nc/4.0/",
					"previews": map[string]string{"preview-hq-mp3": "https://cdn.freesound.org/previews/0/82_1-hq.mp3"}},
			}})
		case r.Host == "cdn.freesound.org":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write(mp3)
		default:
			http.NotFound(w, r)
		}
	}), "freesound.org", "cdn.freesound.org")
	chk := approveStarter(t, a, "freesound-effects", map[string]string{"freesound": key})
	// The API's terms are said where a person decides: the approval page and
	// every plan, beside the ability's own words.
	show := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: "freesound-effects"}, ops.Caller{})
	plan := mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: "freesound-effects", Inputs: map[string]any{"words": "rain"}}, ops.Caller{})
	if !strings.Contains(show.Page.Terms, "non-commercial use only") || !strings.Contains(plan.Terms, "non-commercial use only") || !strings.Contains(show.Ability.Does, "non-commercial") {
		t.Fatalf("Freesound's terms: page %q, plan %q", show.Page.Terms, plan.Terms)
	}
	if !strings.Contains(chk.Test.Log, "would bring in freesound-70002-creaky-door-slow.mp3") || !strings.Contains(chk.Test.Log, "left out “door creak (non-commercial)”") {
		t.Fatalf("the offline check:\n%s", chk.Test.Log)
	}
	out := runStarter(t, a, "freesound-effects", map[string]any{"words": "rain", "count": 2, "longest": 20})
	noKeyIn(t, out, key)
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 1 || !strings.Contains(out.Log, "left out “Rain (NC)”") {
		t.Fatalf("the run: %+v\n%s", out.Run, out.Log)
	}
	o := out.Run.Outputs[0]
	if o.Kind != OutSound || o.Status != "added" || o.Licence != "CC BY 4.0" {
		t.Fatalf("the sound: %+v", o)
	}
	as := assetOf(t, a, o.AssetID)
	if as.Rights == nil || as.Rights.License != "CC BY 4.0" || as.Rights.Attribution != "“Rain on a tin roof” by rain_maker, CC BY 4.0, via Freesound" {
		t.Fatalf("the sound's rights: %+v", as.Rights)
	}
	if k := keyedHosts(out); len(k["freesound"]) != 1 || k["freesound"][0] != "freesound.org" {
		t.Fatalf("where the key went: %v", k)
	}
	for _, r := range svc.asked("cdn.freesound.org") {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("the previews' host got the key")
		}
	}
}

// ---- ComfyUI on this computer (D284) ----

func TestLocalServiceManifest(t *testing.T) {
	base := "ability: 1\nname: local-thing\ntitle: Local thing\ndoes: Draws.\nruns: python3 run.py\noutputs: [picture]\ncost: free\nsends: [nothing]\nneeds: {programs: [python3]}\nlicence: MIT\ntest: {inputs: {}}\n"
	cases := map[string]string{
		"local: {port: 8188, service: ComfyUI}":                     "",
		"local: {port: 4845, service: Sneaky}":                      "MetaRush's own dashboard",
		"local: {port: 27018, service: Sneaky}":                     "a database's port",
		"local: {port: 80, service: Web}":                           "from 1024 to 65535",
		"local: {port: 2375, service: ComfyUI}":                     "Docker's control port",
		"local: {port: 40123, service: ComfyUI}":                    "handed out for a moment",
		"local: {port: 3001, service: ComfyUI}":                     "video renderer",
		"local: {port: 4860, service: ComfyUI}":                     "another installation's",
		"local: {port: 8188, service: \"\"}":                        "say what listens",
		"network: open\nlocal: {port: 8188, service: ComfyUI}":      "an open network never reaches this computer",
		"network: off\nlocal: {port: 8188, service: ComfyUI}":       "its network is off",
		"hosts: [api.example.com]\nlocal: {port: 8188, service: X}": "",
	}
	for extra, want := range cases {
		m, err := ParseManifest([]byte(base + extra + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", extra, err)
		}
		ps := strings.Join(problemTexts(m.Validate(map[string]bool{"run.py": false})), " | ")
		if (want == "") != (ps == "") || !strings.Contains(ps, want) {
			t.Errorf("%s: problems %q, want %q", extra, ps, want)
		}
	}
	m, _ := ParseManifest([]byte(base + "local: {port: 8188, service: ComfyUI}\ninputs: {pic: {kind: picture, optional: true}}\n"))
	if m.NetworkMode() != NetworkMetaRush || m.LeavesComputer() || len(m.Validate(map[string]bool{"run.py": false})) != 0 {
		t.Fatalf("a picture given to a service on this computer doesn't leave it: %v", m.Validate(nil))
	}
}

// comfyIsComfy makes the fake answer the probe as ComfyUI (or not);
// swapAfterPrompt runs after it takes a graph (another program taking the
// port mid-run).
var (
	comfyIsComfy    atomic.Bool
	swapAfterPrompt func()
	// afterSwap marks requests reaching the program swapped onto the port.
	afterSwap  atomic.Bool
	swappedGot atomic.Int64
)

// comfyAnswers sets whether the fake answers the probe as ComfyUI, and
// returns a setter for later.
func comfyAnswers(yes bool) func(bool) {
	comfyIsComfy.Store(yes)
	return func(v bool) { comfyIsComfy.Store(v) }
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// comfyFake plays ComfyUI's HTTP API on "127.0.0.1:8188" (the proxy's dial
// seam sends it here; the real loopback path is TestLocalService's).
func comfyFake(t *testing.T) (*fakeService, *[]map[string]any) {
	comfyIsComfy.Store(true)
	var prompts []map[string]any
	var mu sync.Mutex
	png := pngOf(t)
	svc := fakeHosts(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if afterSwap.Load() {
			swappedGot.Add(1) // a request the program swapped onto the port received
		}
		switch {
		case r.URL.Path == "/system_stats":
			if !comfyIsComfy.Load() {
				writeJSON(w, map[string]any{"hello": "I'm a web server"})
				return
			}
			writeJSON(w, map[string]any{"system": map[string]any{"os": "darwin", "comfyui_version": "0.3.0-fake"}, "devices": []any{}})
		case r.URL.Path == "/object_info/CheckpointLoaderSimple":
			writeJSON(w, map[string]any{"CheckpointLoaderSimple": map[string]any{"input": map[string]any{"required": map[string]any{"ckpt_name": []any{[]string{"example-model.safetensors"}, map[string]any{}}}}}})
		case r.URL.Path == "/prompt" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			prompts = append(prompts, body)
			mu.Unlock()
			if swapAfterPrompt != nil {
				swapAfterPrompt()
				// The program it was is gone: its connections close with it.
				w.Header().Set("Connection", "close")
			}
			writeJSON(w, map[string]any{"prompt_id": "p-1", "number": 1, "node_errors": map[string]any{}})
		case r.URL.Path == "/history/p-1":
			writeJSON(w, map[string]any{"p-1": map[string]any{"status": map[string]any{"status_str": "success", "completed": true},
				"outputs": map[string]any{"9": map[string]any{"images": []any{map[string]string{"filename": "metarush_00001_.png", "subfolder": "", "type": "output"}}}}}})
		case r.URL.Path == "/view" && r.URL.Query().Get("filename") == "metarush_00001_.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		default:
			http.NotFound(w, r)
		}
	}), "http://127.0.0.1:8188")
	return svc, &prompts
}

func pngOf(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 18))
	for x := 0; x < 32; x++ {
		img.Set(x, x%18, color.RGBA{200, 120, 40, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// listening plays this computer's listeners in tests (D284).
func listening(t *testing.T, progs ...models.AbilityLocalProgram) *[]models.AbilityLocalProgram {
	now := append([]models.AbilityLocalProgram{}, progs...)
	testListeners = func(int) []models.AbilityLocalProgram { return now }
	t.Cleanup(func() { testListeners = nil })
	return &now
}

func TestStarterComfyUI(t *testing.T) {
	a := starterApp(t)
	_, prompts := comfyFake(t)
	comfy := models.AbilityLocalProgram{PID: 4242, Name: "python3.12", Path: "/opt/example/python/bin/python3.12", Interpreter: true,
		Cwd: "/opt/example/ComfyUI", Script: "/opt/example/ComfyUI/main.py"}
	now := listening(t)
	localRecheck = 0 // the run's proxy asks on every request
	t.Cleanup(func() { localRecheck = 3 * time.Second })
	chk := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: starterFolder(t, "comfyui-local")}, ops.Caller{})
	if !chk.OK || !strings.Contains(chk.Test.Log, "graph of 7 steps") {
		t.Fatalf("the check: %v\n%+v", problemTexts(chk.Problems), chk.Test)
	}
	ref := chk.Name + "@" + chk.Version
	human := ops.Caller{Human: true}
	// The page says what it reaches on this computer; approving it means agreeing to that.
	show := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: ref}, ops.Caller{})
	if !strings.Contains(show.Page.Local, "ComfyUI on this computer (port 8188)") || !strings.Contains(show.Page.Network, "Only ComfyUI on this computer (port 8188)") ||
		show.Page.Leaves != "Nothing leaves this computer through MetaRush; what you give it goes to ComfyUI on this computer (port 8188), which runs outside MetaRush's sandbox and may send things on by itself." {
		t.Fatalf("the page: %+v", show.Page)
	}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref}, human); err == nil || !strings.Contains(err.Error(), "ComfyUI on this computer") {
		t.Fatalf("approving without agreeing to the service: %v", err)
	}
	// What an owner agrees to is the program listening there: nothing, or
	// MetaRush itself, is refused.
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true}, human); err == nil || !strings.Contains(err.Error(), "Nothing listens on port 8188") {
		t.Fatalf("approving with nothing listening: %v", err)
	}
	*now = []models.AbilityLocalProgram{{PID: os.Getpid(), Name: "abilities.test", Path: "/tmp/abilities.test"}}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true}, human); err == nil || !strings.Contains(err.Error(), "MetaRush's own") {
		t.Fatalf("approving MetaRush's own program: %v", err)
	}
	// The same Python running something else is another program: a plain
	// `python3 -m http.server` started in the home folder isn't ComfyUI.
	server := comfy
	server.Cwd, server.Script = "/Users/you", "-m http.server"
	*now = []models.AbilityLocalProgram{server}
	look := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: ref}, ops.Caller{})
	if look.Local == nil || look.Local.Now == nil || look.Local.NowWords != "“python3.12” (“/opt/example/python/bin/python3.12”), started in “/Users/you”, running “-m http.server”" {
		t.Fatalf("what the owner sees listening now: %+v", look.Local)
	}
	// The owner agrees to what they saw: nothing said, or something else
	// listening by then, is refused.
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true}, human); err == nil || !strings.Contains(err.Error(), "look at the ability first") {
		t.Fatalf("approving without saying which program: %v", err)
	}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true, LocalProgram: &comfy}, human); err == nil || !strings.Contains(err.Error(), "isn't the one you looked at") {
		t.Fatalf("approving a program that isn't listening: %v", err)
	}
	// Code given on the command line, with no probe, can't be told apart.
	noProbe := strings.Replace(mustRead(t, filepath.Join(starterFolder(t, "comfyui-local"), ManifestFile)), "  probe: {path: /system_stats, expect: [system.comfyui_version, devices]}\n", "", 1)
	noProbe = strings.Replace(noProbe, "name: comfyui-local", "name: comfyui-noprobe", 1)
	np := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: noProbe, "make.py": mustRead(t, filepath.Join(starterFolder(t, "comfyui-local"), "make.py"))})}, ops.Caller{})
	code := comfy
	code.Script = ""
	*now = []models.AbilityLocalProgram{code}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: np.Name + "@" + np.Version, LocalService: true, LocalProgram: &code}, human); err == nil || !strings.Contains(err.Error(), "can't tell which program listens") {
		t.Fatalf("an interpreter running unnamed code, no probe: %v", err)
	}
	// A program that doesn't answer the probe as ComfyUI does is refused.
	*now = []models.AbilityLocalProgram{comfy}
	svc := comfyAnswers(false)
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true, LocalProgram: &comfy}, human); err == nil || !strings.Contains(err.Error(), "didn't answer /system_stats's probe") {
		t.Fatalf("a program that doesn't answer the probe: %v", err)
	}
	svc(true)
	d := mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, LocalService: true, LocalProgram: &comfy}, human)
	if v, _ := getVersion(context.Background(), a, d.Version); d.State != models.AbilityApproved || !strings.Contains(d.Message, "“python3.12”") ||
		v.LocalProgram == nil || v.LocalProgram.Script != "/opt/example/ComfyUI/main.py" || v.LocalProgram.Cwd != "/opt/example/ComfyUI" {
		t.Fatalf("approved: %+v", d)
	}
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "comfyui-local"}, human)
	in := AbilityPlanIn{Ref: "comfyui-local", Inputs: map[string]any{"words": "a lighthouse at dawn", "shape": "square", "seed": 7}}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if !p.Ready || p.Decides != DecideLocal || !strings.Contains(p.Leaves, "Nothing leaves this computer") || p.LocalProgram == nil ||
		p.LocalWords != "On port 8188 of this computer: “python3.12”, the program an owner agreed to." || p.LocalProgram.Script != "/opt/example/ComfyUI/main.py" {
		t.Fatalf("the plan: %+v", p)
	}
	// Another program on the port: refused at the plan, and at the run's start.
	run1 := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, ops.Caller{Human: true})
	*now = []models.AbilityLocalProgram{{PID: 77, Name: "nc", Path: "/usr/bin/nc"}}
	if p2 := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{}); p2.Ready || !strings.Contains(strings.Join(p2.Problems, " "), "it is now “nc”, not “python3.12”") {
		t.Fatalf("another program: %+v", p2.Problems)
	}
	// The same Python running another script is another program too.
	*now = []models.AbilityLocalProgram{server}
	if p3 := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{}); p3.Ready || !strings.Contains(strings.Join(p3.Problems, " "), "“python3.12” was started in another folder") {
		t.Fatalf("the same Python, another program: %+v", p3.Problems)
	}
	// Setting it up again for another reason keeps what was agreed; only an
	// owner agreeing, to what they saw, changes it.
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "comfyui-local"}, human)
	if s, _ := getSetup(context.Background(), a, "comfyui-local"); s.LocalProgram != nil {
		t.Fatalf("setting up again recorded the program listening: %+v", s.LocalProgram)
	}
	*now = []models.AbilityLocalProgram{{PID: 77, Name: "nc", Path: "/usr/bin/nc"}}
	if _, err := runJob(t, a, run1.JobID); err == nil {
		t.Fatal("a run whose port now has another program doesn't start")
	}
	if r1 := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run1.ID}, ops.Caller{}); !strings.Contains(r1.Run.Error, "not “python3.12”") || len(*prompts) != 0 {
		t.Fatalf("the refused run: %+v", r1.Run)
	}
	*now = []models.AbilityLocalProgram{comfy}
	// It drives a program outside the sandbox: a person starts each run.
	if _, err := call[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, ops.Caller{}); err == nil {
		t.Fatal("a run reaching ComfyUI needs a person")
	}
	out := runStarter(t, a, "comfyui-local", in.Inputs)
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Status != "added" || out.Run.Outputs[0].Kind != OutPicture {
		t.Fatalf("the run: %+v\n%s", out.Run, out.Log)
	}
	if len(*prompts) != 1 {
		t.Fatalf("ComfyUI got %d graphs", len(*prompts))
	}
	g, _ := (*prompts)[0]["prompt"].(map[string]any)
	ck, _ := g["4"].(map[string]any)["inputs"].(map[string]any)
	if ck["ckpt_name"] != "example-model.safetensors" {
		t.Fatalf("the graph: %v", g)
	}
	for _, q := range out.Requests {
		if q.Host != "127.0.0.1" || q.Port != 8188 || q.Refused != "" {
			t.Fatalf("a request: %+v", q)
		}
	}
	// Another program taking the port while a run waits on it: the run's
	// proxy asks before every new connection — never from memory — and
	// breaks the run off; the program swapped in gets nothing, even with the
	// three seconds' memory for requests on an open connection.
	localRecheck = 3 * time.Second
	swappedGot.Store(0)
	swapAfterPrompt = func() { *now = []models.AbilityLocalProgram{server}; afterSwap.Store(true) }
	t.Cleanup(func() { swapAfterPrompt = nil; afterSwap.Store(false) })
	in2 := AbilityPlanIn{Ref: "comfyui-local", Inputs: map[string]any{"words": "a harbour", "seed": 8}}
	p4 := mustCall[AbilityPlanOut](t, a, "ability.plan", in2, ops.Caller{})
	run4 := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in2, Plan: p4.Fingerprint}, ops.Caller{Human: true})
	if _, err := runJob(t, a, run4.JobID); err == nil {
		t.Fatal("a run whose program changed under it is broken off")
	}
	r4 := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run4.ID}, ops.Caller{})
	if r4.Run.State != models.AbilityRunFailed || !strings.Contains(r4.Run.Error, "It was stopped: On port 8188 of this computer, “python3.12” was started in another folder") {
		t.Fatalf("the broken-off run: %+v", r4.Run)
	}
	if n := swappedGot.Load(); n != 0 {
		t.Fatalf("the program swapped onto the port got %d requests", n)
	}
	swapAfterPrompt = nil
	afterSwap.Store(false)
	localRecheck = 0
	*now = []models.AbilityLocalProgram{comfy}
	// An owner agrees on this computer to what they saw.
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "comfyui-local", AgreeLocal: true, LocalProgram: &comfy}, human)
	if s, _ := getSetup(context.Background(), a, "comfyui-local"); s.LocalProgram == nil || s.LocalProgram.Script != comfy.Script {
		t.Fatalf("agreed on this computer: %+v", s)
	}
	// Taken back, it doesn't reach ComfyUI any more.
	mustCall[AbilityDecisionOut](t, a, "ability.revoke", AbilityRevokeIn{Ref: "comfyui-local"}, human)
	if p := mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: ref, Inputs: in.Inputs}, ops.Caller{}); p.Ready {
		t.Fatal("a revoked version doesn't run")
	}
}

// ---- English captions by whisper.cpp (D285) ----

// ggmlModel is the start of a whisper.cpp model: the magic and its header
// (n_vocab, n_audio_ctx, n_audio_state, n_audio_head, n_audio_layer,
// n_text_ctx, n_text_state, n_text_head, n_text_layer, n_mels, ftype), then
// some weights.
func ggmlModel(vocab, state, textLayers int32) []byte {
	b := make([]byte, 4+11*4)
	binary.LittleEndian.PutUint32(b, 0x67676d6c)
	for i, v := range []int32{vocab, 1500, state, state / 64, textLayers, 448, state, state / 64, textLayers, 80, 1} {
		binary.LittleEndian.PutUint32(b[4+4*i:], uint32(v))
	}
	return append(b, bytes.Repeat([]byte{7}, 4096)...)
}

// fakeWhisper is a stand-in for whisper-cli: it checks it was asked to
// translate, on the CPU, with a model it can read, and writes two lines of
// captions. (The real one aborts in the sandbox unless told -ng: its Metal
// start-up is refused there.)
const fakeWhisper = `#!/bin/sh
tr=""; of=""; model=""; cpu=""
while [ $# -gt 0 ]; do
  case "$1" in
    -tr) tr=1 ;;
    -ng) cpu=1 ;;
    -of) shift; of="$1" ;;
    -m) shift; model="$1" ;;
  esac
  shift
done
[ -n "$tr" ] || { echo "not asked to translate" >&2; exit 3; }
[ -n "$cpu" ] || { echo "the GPU isn't open in the sandbox: run on the CPU (-ng)" >&2; exit 5; }
[ -r "$model" ] || { echo "no model at $model" >&2; exit 4; }
printf '1\n00:00:00,000 --> 00:00:01,500\nWe met in the garden.\n\n2\n00:00:01,500 --> 00:00:03,000\nIt rained all day.\n' > "$of.srt"
echo "translated"
`

func TestStarterEnglishCaptions(t *testing.T) {
	a := starterApp(t)
	for _, p := range []string{"ffmpeg", "whisper-cli"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip("no " + p + " on this computer")
		}
	}
	// The check runs the real ffmpeg and whisper-cli in the sandbox (no clip, no model).
	chk := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: starterFolder(t, "english-captions")}, ops.Caller{})
	if !chk.OK || !strings.Contains(chk.Test.Log, "whisper-cli runs here and can translate") {
		t.Fatalf("the check: %v\n%+v", problemTexts(chk.Problems), chk.Test)
	}
	human := ops.Caller{Human: true}
	mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: chk.Name + "@" + chk.Version}, human)
	show := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: "english-captions"}, ops.Caller{})
	if !strings.Contains(strings.Join(show.Page.Installs, " "), "It reads a whisper-model file a person sets up on each computer") ||
		!strings.Contains(show.Page.Makes, "captions stay with the clip") || show.Page.Leaves != "Nothing leaves this computer." {
		t.Fatalf("the page: %+v", show.Page)
	}
	// Set up with a stand-in for whisper-cli and a model file.
	bin := t.TempDir()
	fake := filepath.Join(bin, "whisper-cli")
	if err := os.WriteFile(fake, []byte(fakeWhisper), 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "ggml-medium.bin")
	if err := os.WriteFile(model, ggmlModel(51865, 1024, 24), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Programs: map[string]string{"whisper-cli": fake}}, human)
	if p.Here.Ready || !strings.Contains(strings.Join(p.Here.Problems, " "), "No whisper-model file is set up on this computer yet") {
		t.Fatalf("without its model: %+v", p.Here)
	}
	if _, err := call[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Files: map[string]string{"other": model}}, human); err == nil {
		t.Fatal("a file it doesn't need")
	}
	// Never a key, MetaRush's own files or a hidden folder's.
	hidden := filepath.Join(t.TempDir(), ".ssh", "id_ed25519")
	_ = os.MkdirAll(filepath.Dir(hidden), 0o700)
	_ = os.WriteFile(hidden, []byte("not a model"), 0o600)
	pem := filepath.Join(t.TempDir(), "model.bin")
	_ = os.WriteFile(pem, []byte("-----BEGIN OPENSSH "+"PRIVATE KEY-----\nabc\n"), 0o600)
	own := filepath.Join(a.Cfg.Home(), "ggml-medium.bin")
	_ = os.WriteFile(own, []byte("model"), 0o600)
	// Models that can't translate are refused by their header, whatever their
	// name: MetaRush's own turbo model sits in its models folder.
	dirM := t.TempDir()
	english, turbo, notModel := filepath.Join(dirM, "ggml-medium-a.bin"), filepath.Join(dirM, "ggml-medium-b.bin"), filepath.Join(dirM, "ggml-medium-c.bin")
	_ = os.WriteFile(english, ggmlModel(51864, 1024, 24), 0o644)
	_ = os.WriteFile(turbo, ggmlModel(51866, 1280, 4), 0o644)
	_ = os.WriteFile(notModel, bytes.Repeat([]byte("model "), 1000), 0o644)
	// Key stores by their first bytes, whatever their name; hard links.
	p12 := filepath.Join(t.TempDir(), "weights.bin")
	_ = os.WriteFile(p12, append([]byte{0x30, 0x82, 0x0a, 0x00, 0x02, 0x01, 0x03, 0x30, 0x82}, make([]byte, 64)...), 0o600)
	keychain := filepath.Join(t.TempDir(), "weights2.bin")
	_ = os.WriteFile(keychain, append([]byte("kych\x00\x01"), make([]byte, 64)...), 0o600)
	cookies := filepath.Join(t.TempDir(), "weights3.bin")
	_ = os.WriteFile(cookies, append([]byte("SQLite format 3\x00"), make([]byte, 64)...), 0o600)
	linked := filepath.Join(t.TempDir(), "ggml-medium-link.bin")
	if err := os.Link(model, linked); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{hidden: "hidden file or folder", pem: "looks like it holds a private key", own: "MetaRush's own folders",
		english: "English-only", turbo: "turbo model", notModel: "isn't a whisper.cpp model", p12: "private key or a certificate store", keychain: "keychain",
		cookies: "a database", linked: "more than one name"}
	for path, why := range cases {
		if _, err := call[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Files: map[string]string{"whisper-model": path}}, human); err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("giving %s: %v", path, err)
		}
	}
	_ = os.Remove(linked)
	// Looking first says where the file really is and what it is, in
	// MetaRush's words beside the ability's.
	look := mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Files: map[string]string{"whisper-model": model}, Look: true}, human)
	if len(look.Files) != 1 || look.Files[0].About != "a whisper.cpp model (medium, multilingual)" || !strings.HasSuffix(look.Files[0].Path, "ggml-medium.bin") || look.Here.Files[0].SetUp {
		t.Fatalf("looking at the file: %+v", look)
	}
	// A file goes into every run, like a key: an owner chooses it.
	member := ops.OverHTTP(context.Background(), func() bool { return false })
	raw, _ := json.Marshal(AbilitySetupIn{Ref: "english-captions", Files: map[string]string{"whisper-model": model}})
	if _, err := ops.Call(member, a, "ability.setup", raw, ops.Caller{Human: true, Remote: true}); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("a member setting up a file: %v", err)
	}
	p = mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Programs: map[string]string{"whisper-cli": fake}, Files: map[string]string{"whisper-model": model}}, human)
	if !p.Here.Ready || len(p.Here.Files) != 1 || !p.Here.Files[0].OK {
		t.Fatalf("set up: %+v", p.Here)
	}
	// Setting it up again without naming the file keeps it.
	if p = mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "english-captions", Programs: map[string]string{"whisper-cli": fake}}, human); !p.Here.Ready {
		t.Fatalf("set up again: %+v", p.Here)
	}
	// A clip spoken in another language.
	clip := filepath.Join(t.TempDir(), "entrevista.mp4")
	_ = os.WriteFile(clip, testMP4(t), 0o644)
	res := ingest.File(context.Background(), a, clip, ingest.Options{Via: "cli", Title: "Entrevista", FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("the clip: %+v", res)
	}
	in := AbilityPlanIn{Ref: "english-captions", Inputs: map[string]any{"clip": res.AssetID, "language": "es"}}
	plan := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if !plan.Ready || plan.Decides != DecideSafe || plan.Where != "Captions stay with the clip they were made from." {
		t.Fatalf("the plan: %+v", plan)
	}
	// Free, local, nothing leaves: anyone starts it, Claude in a terminal too.
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: plan.Fingerprint}, ops.Caller{})
	if _, err := runJob(t, a, run.JobID); err != nil {
		t.Fatal(err)
	}
	out := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{})
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Kind != OutCaptions || out.Run.Outputs[0].Status != "added" || out.Run.Outputs[0].AssetID != res.AssetID {
		t.Fatalf("the run: %+v\n%s", out.Run, out.Log)
	}
	as := assetOf(t, a, res.AssetID)
	if len(as.Captions) != 1 || as.Captions[0].Language != "en" || !as.Captions[0].Translated || as.Captions[0].Lines != 2 || as.Captions[0].MadeWith == nil || as.Captions[0].MadeWith.Name != "english-captions" ||
		as.Captions[0].Model != "whisper.cpp medium (multilingual), translated from es" {
		t.Fatalf("the clip's captions: %+v", as.Captions)
	}
	list := mustCall[ops.FootageCaptionsOut](t, a, "footage.captions", ops.FootageCaptionsIn{AssetID: res.AssetID}, ops.Caller{})
	if len(list.Files) != 1 || list.Files[0].Name != "English" || list.Text != "" {
		t.Fatalf("footage.captions: %+v", list)
	}
	en := mustCall[ops.FootageCaptionsOut](t, a, "footage.captions", ops.FootageCaptionsIn{AssetID: res.AssetID, Language: "en"}, ops.Caller{})
	if !strings.Contains(en.Text, "We met in the garden.") || en.Format != "srt" || !strings.Contains(en.Note, "machine-translated") {
		t.Fatalf("the English captions: %+v", en)
	}
	// Running it again replaces them (one English file from this ability).
	plan = mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	run = mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: plan.Fingerprint}, ops.Caller{})
	if _, err := runJob(t, a, run.JobID); err != nil {
		t.Fatal(err)
	}
	if as = assetOf(t, a, res.AssetID); len(as.Captions) != 1 {
		t.Fatalf("captions after a second run: %+v", as.Captions)
	}
	// A model changed since it was set up stops the next run until a person sets it up again.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(model, []byte("another model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if plan = mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{}); plan.Ready || !strings.Contains(strings.Join(plan.Problems, " "), "changed since it was set up") {
		t.Fatalf("a changed model: %+v", plan.Problems)
	}
}

// ---- not yet runnable: Content Credentials (D286) and DaVinci Resolve (D287) ----

// TestStarterNotYetRunnable keeps the two abilities MetaRush can't run yet
// honest: their check is refused for exactly the things notes.md says are
// missing, and the rest of each manifest is sound. When MetaRush grows one of
// them, this fails and says to move the ability on.
func TestStarterNotYetRunnable(t *testing.T) {
	a := starterApp(t)
	cases := []struct {
		name    string
		missing []string // manifest lines MetaRush can't read yet
		rest    []string // what Validate still says without them
	}{
		{"content-credentials", []string{"signs:"}, []string{"inputs.video: kind is one of", "\"delivered-copy\" isn't something MetaRush takes in"}},
		{"davinci-resolve", []string{"serves:", "tools:", "app:"}, nil},
	}
	for _, c := range cases {
		dir := starterFolder(t, c.name)
		b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(b), "# NOT YET RUNNABLE") {
			t.Errorf("%s's manifest says it isn't runnable yet in its first line", c.name)
		}
		out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: dir}, ops.Caller{})
		said := strings.Join(problemTexts(out.Problems), " | ")
		if out.OK || out.Recorded || !strings.Contains(said, strings.TrimSuffix(c.missing[0], ":")+" isn't a field a manifest has") {
			t.Fatalf("%s: the check refuses what MetaRush can't run yet: %s", c.name, said)
		}
		var kept []string
		for _, line := range strings.Split(string(b), "\n") {
			skip := false
			for _, m := range c.missing {
				skip = skip || strings.HasPrefix(line, m)
			}
			if !skip {
				kept = append(kept, line)
			}
		}
		m, err := ParseManifest([]byte(strings.Join(kept, "\n")))
		if err != nil {
			t.Fatalf("%s without %v: %v", c.name, c.missing, err)
		}
		arc, err := Build(dir)
		if err != nil {
			t.Fatal(err)
		}
		ps := problemTexts(m.Validate(arc.Programs()))
		if len(ps) != len(c.rest) {
			t.Fatalf("%s without %v says %v, want %v", c.name, c.missing, ps, c.rest)
		}
		for i, want := range c.rest {
			if !strings.Contains(ps[i], want) {
				t.Errorf("%s: %q, want %q", c.name, ps[i], want)
			}
		}
	}
}

// TestStarterPackIsWhole: the folder has the seven starter abilities and the
// three people-only posting abilities (D307, not yet runnable), each with its
// notes, each in the README's table, and each runnable one tested above.
func TestStarterPackIsWhole(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", "..", "abilities"))
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	readme, _ := os.ReadFile(filepath.Join(root, "README.md"))
	want := []string{"comfyui-local", "content-credentials", "davinci-resolve", "english-captions", "freesound-effects", "linkedin-video", "open-media", "pexels-stock",
		"podcast-episode", "vimeo-upload"}
	var got []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		got = append(got, e.Name())
		if _, err := os.Stat(filepath.Join(root, e.Name(), NotesFile)); err != nil {
			t.Errorf("%s has no notes.md", e.Name())
		}
		if !strings.Contains(string(readme), "`"+e.Name()+"`") {
			t.Errorf("the README's table doesn't list %s", e.Name())
		}
		// A name means one thing in find, describe, run and the menu (D268):
		// never a first-party ability's, now or retired.
		if catalog.IsFirstPartyID(e.Name()) {
			t.Errorf("%s is a first-party ability's name", e.Name())
		}
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the starter pack: %v, want %v", got, want)
	}
}

// TestStarterC2patoolInstall: the install script takes only the pinned
// download, from a fake release on this computer (never GitHub).
func TestStarterC2patoolInstall(t *testing.T) {
	for _, p := range []string{"curl", "unzip", "tar"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip("no " + p + " on this computer")
		}
	}
	name, archive := fakeC2patoolArchive(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	script := filepath.Join(starterFolder(t, "content-credentials"), "install-c2patool.sh")
	home := t.TempDir()
	install := func(env ...string) (string, string, error) {
		prefix := filepath.Join(t.TempDir(), "c2patool")
		cmd := exec.Command("sh", script, "--prefix", prefix)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "TMPDIR=" + t.TempDir()}, env...)
		b, err := cmd.CombinedOutput()
		return prefix, string(b), err
	}
	// The real pin refuses the fake.
	prefix, said, err := install("C2PATOOL_BASE_URL=" + srv.URL)
	if err == nil || !strings.Contains(said, "isn't the c2patool 0.28.3 this script pins") {
		t.Fatalf("a download that isn't the pinned one: %v %s", err, said)
	}
	if _, err := os.Stat(prefix); err == nil {
		t.Fatal("nothing is installed when the download isn't the pinned one")
	}
	// A test pin only with a download from this computer.
	sum := sha256.Sum256(archive)
	if _, said, err := install("C2PATOOL_BASE_URL=https://example.invalid/dl", "C2PATOOL_TEST_PIN="+hex.EncodeToString(sum[:])); err == nil || !strings.Contains(said, "only for a test download") {
		t.Fatalf("a test pin elsewhere: %v %s", err, said)
	}
	prefix, said, err = install("C2PATOOL_BASE_URL="+srv.URL, "C2PATOOL_TEST_PIN="+hex.EncodeToString(sum[:]))
	if err != nil || !strings.Contains(said, "c2patool 0.28.3 (fake)") || !strings.Contains(said, "metarush ability setup content-credentials --program c2patool="+prefix+"/c2patool") {
		t.Fatalf("the pinned install: %v %s", err, said)
	}
	if st, err := os.Stat(filepath.Join(prefix, "c2patool")); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("the program: %v", err)
	}
	// An address that only starts like this computer's names another host:
	// the real pins and HTTPS stay.
	if _, said, err := install("C2PATOOL_BASE_URL=http://127.0.0.1:1@evil.example/dl", "C2PATOOL_TEST_PIN="+hex.EncodeToString(sum[:])); err == nil || !strings.Contains(said, "only for a test download") {
		t.Fatalf("an address with a user part: %v %s", err, said)
	}
	// A link planted where the program goes is replaced, never written through.
	victim := filepath.Join(t.TempDir(), "victim")
	_ = os.WriteFile(victim, []byte("keep me"), 0o644)
	planted := filepath.Join(t.TempDir(), "planted")
	_ = os.MkdirAll(planted, 0o755)
	if err := os.Symlink(victim, filepath.Join(planted, "c2patool")); err != nil {
		t.Fatal(err)
	}
	cmd2 := exec.Command("sh", script, "--prefix", planted)
	cmd2.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "TMPDIR=" + t.TempDir(), "C2PATOOL_BASE_URL=" + srv.URL, "C2PATOOL_TEST_PIN=" + hex.EncodeToString(sum[:])}
	if b, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("installing over a planted link: %v %s", err, b)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me" {
		t.Fatalf("the link's target was written: %q", got)
	}
	if st, err := os.Lstat(filepath.Join(planted, "c2patool")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the program replaced the link: %v", err)
	}
	// Never inside the home folder.
	cmd := exec.Command("sh", script, "--prefix", filepath.Join(home, "bin"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "inside your home folder") {
		t.Fatalf("a home folder: %v %s", err, b)
	}
}

// fakeC2patoolArchive is a release archive for this computer holding a
// stand-in c2patool, named as the script expects.
func fakeC2patoolArchive(t *testing.T) (string, []byte) {
	prog := "#!/bin/sh\necho 'c2patool 0.28.3 (fake)'\n"
	var b bytes.Buffer
	if runtime.GOOS == "darwin" {
		z := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: "c2patool/c2patool", Method: zip.Deflate}
		h.SetMode(0o755)
		w, _ := z.CreateHeader(h)
		_, _ = w.Write([]byte(prog))
		_ = z.Close()
		return "v0.28.3-universal-apple-darwin.zip", b.Bytes()
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("c2patool 0.28.3 has no download for this computer")
	}
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "c2patool/c2patool", Mode: 0o755, Size: int64(len(prog)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(prog))
	_ = tw.Close()
	_ = gz.Close()
	return "v0.28.3-x86_64-unknown-linux-gnu.tar.gz", b.Bytes()
}

// fakeComfyScript is a fake ComfyUI in a process of its own (a real
// program listening on a real port, as MetaRush looks it up).
const fakeComfyScript = `import base64, http.server, json, sys
PNG = base64.b64decode(sys.argv[3])
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def send(self, code, body, ctype="application/json"):
        self.send_response(code); self.send_header("Content-Type", ctype); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def do_GET(self):
        if self.path == "/system_stats":
            return self.send(200, json.dumps({"system": {"comfyui_version": "0.3.0-fake"}, "devices": []}).encode())
        if self.path.startswith("/object_info/"):
            return self.send(200, json.dumps({"CheckpointLoaderSimple": {"input": {"required": {"ckpt_name": [["m.safetensors"], {}]}}}}).encode())
        if self.path.startswith("/history/"):
            return self.send(200, json.dumps({"p-1": {"status": {"status_str": "success", "completed": True}, "outputs": {"9": {"images": [{"filename": "x.png", "subfolder": "", "type": "output"}]}}}}).encode())
        if self.path.startswith("/view"):
            return self.send(200, PNG, "image/png")
        self.send(404, b"{}")
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        open(sys.argv[2], "a").write("prompt\\n")
        self.send(200, json.dumps({"prompt_id": "p-1"}).encode())
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
`

// freeServicePort is a free port an owner may let an ability reach (below
// the range handed out for a moment).
func freeServicePort(t *testing.T) int {
	for p := 20000 + int(time.Now().UnixNano()%5000); p < 29999; p++ {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			ln.Close()
			return p
		}
	}
	t.Skip("no free port")
	return 0
}

// TestStarterComfyUIForReal runs the ComfyUI ability with no seams: a
// version whose one local service is a fake ComfyUI in its own process on a
// real port; MetaRush looks up the program listening there (lsof on a Mac,
// /proc on Linux), the owner's approval notes it, and the run reaches it from
// the sandbox through the run's proxy (D284).
func TestStarterComfyUIForReal(t *testing.T) {
	a := starterApp(t)
	if runtime.GOOS == "darwin" && !a.Tools.Has("lsof") {
		t.Skip("no lsof")
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	script, seen := filepath.Join(dir, "fake_comfy.py"), filepath.Join(dir, "prompts.txt")
	_ = os.WriteFile(script, []byte(fakeComfyScript), 0o644)
	port := freeServicePort(t)
	cmd := exec.Command(py, "-I", script, fmt.Sprint(port), seen, base64.StdEncoding.EncodeToString(pngOf(t)))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	src := starterFolder(t, "comfyui-local")
	files := map[string]string{}
	for _, f := range []string{ManifestFile, "make.py", NotesFile} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		files[f] = strings.Replace(string(b), "  port: 8188\n", fmt.Sprintf("  port: %d\n", port), 1)
	}
	chk := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, files)}, ops.Caller{})
	if !chk.OK {
		t.Fatalf("the check: %v", problemTexts(chk.Problems))
	}
	human := ops.Caller{Human: true}
	show := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: chk.Name + "@" + chk.Version}, ops.Caller{})
	if show.Local == nil || show.Local.Now == nil || !show.Local.Now.Interpreter || !strings.HasSuffix(show.Local.Now.Script, "fake_comfy.py") || show.Local.Now.Cwd == "" {
		t.Fatalf("the program listening, as the owner sees it: %+v", show.Local)
	}
	d := mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: chk.Name + "@" + chk.Version, LocalService: true, LocalProgram: show.Local.Now}, human)
	v, _ := getVersion(context.Background(), a, d.Version)
	if v.LocalProgram == nil || v.LocalProgram.Path == "" || !strings.Contains(strings.ToLower(v.LocalProgram.Name), "python") || !strings.HasSuffix(v.LocalProgram.Script, "fake_comfy.py") {
		t.Fatalf("the program the owner agreed to: %+v", v.LocalProgram)
	}
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "comfyui-local"}, human)
	out := runStarter(t, a, "comfyui-local", map[string]any{"words": "a lighthouse"})
	got, _ := os.ReadFile(seen)
	if out.Run.State != models.AbilityRunDone || len(out.Run.Outputs) != 1 || out.Run.Outputs[0].Status != "added" || strings.Count(string(got), "prompt") != 1 {
		t.Fatalf("the run: %+v %q\n%s\n%+v", out.Run, got, out.Log, out.Requests)
	}
	for _, q := range out.Requests {
		if q.Port != port {
			t.Fatalf("a request's port: %+v", q)
		}
	}
}
