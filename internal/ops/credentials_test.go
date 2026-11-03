package ops_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakec2patool"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestMain(m *testing.M) {
	c2pa.MaybeSigner()      // c2patool's signer is this binary (D416)
	fakec2patool.MaybeRun() // and so is the stand-in c2patool
	os.Exit(m.Run())
}

// credentialsApp is a test app whose keychain is a stand-in (never this
// computer's) and whose c2patool is the stand-in with st; it returns the
// stand-in's log of calls (arguments and environment).
func credentialsApp(t *testing.T, st fakec2patool.State) (*app.App, string) {
	t.Helper()
	keyring.MockInit()
	a := testapp.New(t)
	st.Log = filepath.Join(t.TempDir(), "c2patool-calls.jsonl")
	tool, _ := fakec2patool.Write(t, st)
	a.Cfg.Tools.C2PATool = tool
	a.Tools.Rescan(a.Cfg.Tools)
	if err := a.DB.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	return a, st.Log
}

// credentialsAppState is credentialsApp with the stand-in's state file, to
// change what it does between steps.
func credentialsAppState(t *testing.T, st fakec2patool.State) (*app.App, string) {
	t.Helper()
	keyring.MockInit()
	a := testapp.New(t)
	tool, state := fakec2patool.Write(t, st)
	a.Cfg.Tools.C2PATool = tool
	a.Tools.Rescan(a.Cfg.Tools)
	if err := a.DB.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	return a, state
}

// approvedWithRender is a version of a new project, approved, with a final
// render (and, checked, a quality check that found nothing).
func approvedWithRender(t *testing.T, a *app.App, name string, check bool) (models.Project, string, models.Render) {
	t.Helper()
	p := project(t, a, name)
	as := txClip(t, a, "Interview with Ada Lovelace", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("there.", 0.7, 2.2, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: rid})
	rd := finalRender(t, a, rid)
	if check {
		checked(t, a, rd, nil, nil)
	}
	return p, rid, rd
}

// broughtCredential is a certificate an authority issued for signing
// documents, and its key, as PEM (made here; nothing is ever committed).
func broughtCredential(t *testing.T) (chainPEM, keyPEM string, key *ecdsa.PrivateKey) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "Example CA", Organization: []string{"Example Authority"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(240 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "Example Studio signing", Organization: []string{"Example Studio"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(240 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, key.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	chainPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return chainPEM, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})), key
}

// secretForms are the written forms of a private key a leak would take.
func secretForms(key *ecdsa.PrivateKey) []string {
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	b64 := base64.StdEncoding.EncodeToString(kder)
	forms := []string{b64, key.D.Text(16), key.D.String(), hex.EncodeToString(kder)}
	for i := 0; i+40 <= len(b64); i += 40 {
		forms = append(forms, b64[i:i+40])
	}
	return forms
}

// everything is every document of the test database, as JSON.
func everything(t *testing.T, a *app.App) string {
	t.Helper()
	ctx := context.Background()
	a.Log.Flush(ctx)
	names, err := a.DB.DB.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, n := range names {
		cur, err := a.DB.DB.Collection(n).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		_ = cur.All(ctx, &docs)
		j, _ := json.Marshal(docs)
		b.Write(j)
	}
	return b.String()
}

// Setting up a signing certificate is an owner's, a person's (D417); the key
// reaches no command line, environment, log, error or the database.
func TestCredentialsSetupIsAPersonsAndAnOwners(t *testing.T) {
	a, log := credentialsApp(t, fakec2patool.State{})
	ctx := context.Background()
	chain, key, priv := broughtCredential(t)
	in := ops.CredentialsSetupIn{Certificate: chain, PrivateKey: key}
	if err := doErr(a, "credentials.setup", in, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("not a person: %v", err)
	}
	member := ops.OverHTTP(ctx, func() bool { return false })
	b, _ := json.Marshal(in)
	if _, err := ops.Call(member, a, "credentials.setup", b, ops.Caller{Human: true, Remote: true}); code(err) != "E_UNAUTHORIZED" {
		t.Fatalf("a member: %v", err)
	}
	panel := *a
	panel.Actor.Via = models.ViaAssistant
	if err := doErr(&panel, "credentials.setup", in, person); err == nil {
		t.Fatal("Claude in the panel set it up")
	}
	// A key that doesn't belong, then a broken key: plain errors that quote neither file.
	_, other, _ := broughtCredential(t)
	for _, bad := range []ops.CredentialsSetupIn{{Certificate: chain, PrivateKey: other}, {Certificate: chain, PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{0, 0, 0}}))}} {
		err := doErr(a, "credentials.setup", bad, person)
		if code(err) != "E_INVALID_INPUT" || strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), other[30:60]) {
			t.Fatalf("a bad key: %v", err)
		}
	}
	out := do[ops.CredentialsSetupOut](t, a, "credentials.setup", in)
	h := out.Status.Here
	if !h.Ready || h.Certificate == nil || h.Certificate.Signer != "Example Studio" || h.Certificate.Test || h.Kept != c2pa.KeptKeychain || out.Tried == nil || !out.Tried.Verified {
		t.Fatalf("set up: %+v %+v", out, h)
	}
	if err := doErr(a, "credentials.setup", ops.CredentialsSetupIn{Test: true}, person); code(err) != "E_CONFLICT" {
		t.Fatalf("set up twice without replacing: %v", err)
	}
	for _, l := range append(out.Status.Lines, out.Summary, h.Says) {
		if fs := plainwords.CheckShown(l); fs != nil {
			t.Errorf("%q: %v", l, fs)
		}
	}
	// A delivery signs with it.
	do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn})
	_, rid, _ := approvedWithRender(t, a, "Garden story", false)
	dlv := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if c := dlv.Files[0].ContentCredentials; c == nil || !c.Signed || c.Signer != "Example Studio" {
		t.Fatalf("delivery: %+v", dlv.Files[0])
	}
	// Nowhere but the keychain: not the database (log and audit included),
	// not c2patool's arguments or environment, not the home's files.
	all := everything(t, a)
	calls, _ := json.Marshal(fakec2patool.Calls(log))
	var files strings.Builder
	_ = filepath.WalkDir(a.Cfg.Home(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			files.Write(b)
		}
		return nil
	})
	for _, f := range secretForms(priv) {
		for where, s := range map[string]string{"the database": all, "c2patool's arguments and environment": string(calls), "the home's files": files.String()} {
			if strings.Contains(s, f) {
				t.Fatalf("the key is in %s: %.16s…", where, f)
			}
		}
	}
	if !strings.Contains(all, "credentials.setup") {
		t.Fatal("the setup isn't in the activity log or the audit")
	}
	// Removing it is a person's too.
	if err := doErr(a, "credentials.remove", struct{}{}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent removed it: %v", err)
	}
	st := do[ops.CredentialsStatusOut](t, a, "credentials.remove", struct{}{})
	if st.Here.Ready || st.Here.Certificate != nil {
		t.Fatalf("after removing: %+v", st.Here)
	}
}

// A delivered final video carries Content Credentials (D418–D420): signed,
// read back and checked, its record, notes file, version and receipt say
// so, and the manifest names no project, clip or person.
func TestDeliveryCarriesContentCredentials(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	ctx := context.Background()
	p, rid, rd := approvedWithRender(t, a, "Acme launch teaser", false)
	// Off: delivery is as it always was.
	off := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if off.Files[0].ContentCredentials != nil || off.Files[0].SHA256 != rd.Output.SHA256 || strings.Contains(off.Note, "Content Credentials") {
		t.Fatalf("off: %+v", off.Files[0])
	}
	do[ops.CredentialsSetupOut](t, a, "credentials.setup", ops.CredentialsSetupIn{Test: true})
	if err := doErr(a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn}, ops.Caller{}); err != nil {
		t.Fatalf("an owner's terminal turns them on: %v", err)
	}
	dir := t.TempDir()
	out := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir})
	f := out.Files[0]
	c := f.ContentCredentials
	if c == nil || !c.Signed || !c.Verified || !c.TestCertificate || c.ManifestHash == "" || c.Required {
		t.Fatalf("credentials: %+v", c)
	}
	b, _ := os.ReadFile(f.Path)
	sum := sha256.Sum256(b)
	if f.SHA256 != hex.EncodeToString(sum[:]) || f.SHA256 == rd.Output.SHA256 || f.Size != int64(len(b)) || c.SHA256 != f.SHA256 {
		t.Fatalf("the delivered file's hash %s, the render's %s, said %s", hex.EncodeToString(sum[:]), rd.Output.SHA256, f.SHA256)
	}
	if h, err := c2pa.ManifestHash(f.Path); err != nil || h != c.ManifestHash {
		t.Fatalf("manifest hash %s %v, said %s", h, err, c.ManifestHash)
	}
	// (Needles long enough not to turn up by chance in a random render or base64.)
	for _, never := range []string{"launch", "teaser", "lovelace", "interview", strings.ToLower(strings.TrimSuffix(filepath.Base(f.Path), ".mp4"))} {
		if strings.Contains(strings.ToLower(string(b)), never) {
			t.Errorf("the delivered file's credentials say %q", never)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Fatalf("hidden files left: %v", left)
	}
	var sc ops.DeliverySidecar
	sb, _ := os.ReadFile(f.Sidecar)
	if err := json.Unmarshal(sb, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.ContentCredentials == nil || sc.ContentCredentials.ManifestHash != c.ManifestHash || sc.Render.SHA256 != rd.Output.SHA256 {
		t.Fatalf("record: %+v", sc.ContentCredentials)
	}
	for _, k := range []string{`"signed": true`, `"signer"`, `"verified": true`, `"manifestHash"`} {
		if !strings.Contains(string(sb), k) {
			t.Errorf("the record lacks %s", k)
		}
	}
	notes, err := os.ReadFile(filepath.Join(dir, sc.NotesFile))
	if err != nil || !strings.Contains(string(notes), "This video carries Content Credentials") || !strings.Contains(string(notes), "test certificate") {
		t.Fatalf("notes %q: %v", notes, err)
	}
	if !strings.Contains(out.Note, "carries Content Credentials") {
		t.Fatalf("note: %s", out.Note)
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &rev); err != nil || len(rev.ContentCredentials) != 1 || rev.ContentCredentials[0].ManifestHash != c.ManifestHash || rev.ContentCredentials[0].File != filepath.Base(f.Path) {
		t.Fatalf("version: %+v %v", rev.ContentCredentials, err)
	}
	rc, err := receipt.Receipt(ctx, a, &p, &rev)
	if err != nil || len(rc.Signed) != 1 || !strings.Contains(strings.Join(rc.Lines, " "), "went out with Content Credentials") {
		t.Fatalf("receipt: %+v %v", rc.Lines, err)
	}
	// Checking the file reads them back: ours, and they check out.
	chk := do[ops.CredentialsCheckOut](t, a, "credentials.check", ops.CredentialsCheckIn{Path: f.Path})
	if !chk.Carries || !chk.Verified || !chk.Ours || chk.ManifestHash != c.ManifestHash {
		t.Fatalf("check: %+v", chk)
	}
	for _, l := range []string{c.Says, out.Note, chk.Summary} {
		if fs := plainwords.CheckShown(l); fs != nil {
			t.Errorf("%q: %v", l, fs)
		}
	}
	// Review copies never carry them.
	var rev2 models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &rev2)
	if err := a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), rd.ID, bson.M{"$set": bson.M{"quality": "review"}}); err != nil {
		t.Fatal(err)
	}
	rv := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir(), Quality: "review"})
	if c := rv.Files[0].ContentCredentials; c == nil || c.Signed || !strings.Contains(c.Says, "Review copies don't carry") {
		t.Fatalf("a review copy: %+v", c)
	}
}

// Without a certificate on this computer, a delivery where they are on goes
// out without them and says why; a copy that doesn't check out after
// signing never goes out (D420).
func TestDeliveryWithoutWorkingCredentials(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	_, rid, rd := approvedWithRender(t, a, "Garden story", false)
	do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn})
	dir := t.TempDir()
	out := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir})
	c := out.Files[0].ContentCredentials
	if c == nil || c.Signed || !strings.Contains(c.Problem, "no signing certificate") || out.Files[0].SHA256 != rd.Output.SHA256 {
		t.Fatalf("no certificate: %+v", c)
	}
	if !strings.Contains(strings.Join(out.Warnings, " "), "went out without Content Credentials") || !strings.Contains(out.Note, "went out without Content Credentials") {
		t.Fatalf("not said: %v / %s", out.Warnings, out.Note)
	}

	// A copy that doesn't check out: the render goes out instead, and says so.
	bad, _ := credentialsApp(t, fakec2patool.State{TamperAfterSign: true})
	_, rid2, rd2 := approvedWithRender(t, bad, "Garden story", false)
	do[ops.CredentialsSetupOut](t, bad, "credentials.setup", ops.CredentialsSetupIn{Test: true})
	do[ops.CredentialsStatusOut](t, bad, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn})
	dir2 := t.TempDir()
	out = do[ops.DeliverOut](t, bad, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid2}, Out: dir2})
	c = out.Files[0].ContentCredentials
	if c == nil || c.Signed || c.Verified || !strings.Contains(c.Problem, "didn't check out after signing") || out.Files[0].SHA256 != rd2.Output.SHA256 {
		t.Fatalf("a copy that didn't check out: %+v", c)
	}
	var sc ops.DeliverySidecar
	sb, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(sb, &sc)
	if sc.ContentCredentials == nil || sc.ContentCredentials.Signed || !strings.Contains(sc.ContentCredentials.Says, "went out without Content Credentials") {
		t.Fatalf("record: %+v", sc.ContentCredentials)
	}
	if left, _ := filepath.Glob(filepath.Join(dir2, ".*")); len(left) > 0 {
		t.Fatalf("the copy that didn't check out was left: %v", left)
	}
	// Required (the gate off): nothing is delivered at all. This computer
	// remembers that its last signing failed (D437), so the version is held
	// before anything is tried; a sample that works lets it try again.
	do[ops.CredentialsStatusOut](t, bad, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsRequired})
	dir3 := t.TempDir()
	err := doErr(bad, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid2}, Out: dir3}, person)
	if code(err) != "E_HELD" || !strings.Contains(err.Error(), "must carry Content Credentials") || !strings.Contains(err.Error(), "the last signing on this computer failed") {
		t.Fatalf("required, after a signing that didn't check out: %v", err)
	}
	if entries, _ := os.ReadDir(dir3); len(entries) != 0 {
		t.Fatalf("written anyway: %v", entries)
	}
}

// Required, with the gate on (D418): a version this computer can't sign is
// held; a person may send it out without them with a note; once a
// certificate is set up it goes out signed.
func TestShipGateCanRequireContentCredentials(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	ctx := context.Background()
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: true})
	p, rid, _ := approvedWithRender(t, a, "Garden story", true)
	ready := func() *ops.ShipGateOut {
		return do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	}
	if g := ready(); !g.Ready {
		t.Fatalf("off: %+v", g)
	}
	// A client's Content Settings require them; an agent can't ask less.
	do[ops.CredentialsStatusOut](t, a, "content.credentials.set", ops.ContentCredentialsSetIn{ProjectID: p.ID, Mode: models.CredentialsRequired})
	if err := doErr(a, "content.credentials.set", ops.ContentCredentialsSetIn{ProjectID: p.ID, Mode: models.CredentialsOff}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent asked less: %v", err)
	}
	if mode, whose := ops.CredentialsFor(ctx, a, p.ID); mode != models.CredentialsRequired || !strings.Contains(whose, "this project's settings") {
		t.Fatalf("for the project: %s %s", mode, whose)
	}
	g := ready()
	if g.Ready || len(g.Holds) != 1 || g.Holds[0].Kind != models.HoldCredentials || g.Holds[0].Key != "credentials:none" || !g.Holds[0].Overridable ||
		g.Holds[0].Paths[0].Link != "/settings/delivery#content-credentials" {
		t.Fatalf("held: %+v", g)
	}
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()}, person); code(err) != "E_HELD" || !strings.Contains(err.Error(), "can't sign them") {
		t.Fatalf("delivered: %v", err)
	}
	// "Required" is its own choice: with the gate off it holds the version
	// too (and nothing else does), so the page and delivery agree.
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: false})
	off := ready()
	if off.Ready || off.State != ops.GateHeld || len(off.Holds) != 1 || !off.Holds[0].Anyway || !strings.Contains(off.Summary, "must carry Content Credentials") {
		t.Fatalf("held with the gate off: %+v", off)
	}
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()}, person); code(err) != "E_HELD" || !strings.Contains(err.Error(), "must carry Content Credentials") {
		t.Fatalf("delivered with the gate off: %v", err)
	}
	// A timeline carries none: not held for them.
	if tg, err := ops.TimelineGate(ctx, a, &p, mustRevision(t, a, rid)); err != nil || holdOf(tg, models.HoldCredentials) {
		t.Fatalf("a timeline is held for Content Credentials: %+v %v", tg, err)
	}
	// A person sends it out without them, with a note — with the gate off too.
	if err := doErr(a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "The client agreed to this one without them.", Holds: shownKeys(off)}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent sent it anyway: %v", err)
	}
	sent := do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "The client agreed to this one without them.", Holds: shownKeys(off)})
	if !sent.Ready {
		t.Fatalf("sent anyway: %+v", sent)
	}
	out := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if c := out.Files[0].ContentCredentials; c == nil || c.Signed || !c.Required || !strings.Contains(c.Problem, "with a note") || len(out.Files[0].SentAnyway) != 1 {
		t.Fatalf("sent without them: %+v", out.Files[0])
	}
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: true})
	if g := ready(); !g.Ready || g.State != ops.GateSentAnyway {
		t.Fatalf("the note covers it with the gate on too: %+v", g)
	}
	// Set up: nothing holds it, and it goes out signed.
	do[ops.CredentialsSetupOut](t, a, "credentials.setup", ops.CredentialsSetupIn{Test: true, Organisation: "Example Studio"})
	if g := ready(); holdOf(g, models.HoldCredentials) {
		t.Fatalf("still held: %+v", g)
	}
	out = do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if c := out.Files[0].ContentCredentials; c == nil || !c.Signed || !c.Required || c.Signer != "Example Studio" {
		t.Fatalf("signed: %+v", c)
	}
}

// Required, with the gate on and a certificate (D437): a signing that fails
// during the delivery stops it and is remembered on this computer, so the
// version is held for it (Try it on a sample); a person can send it out
// without them with a note; a signing that works again lets it go signed.
func TestARequiredSigningThatFailsCanGoOutWithANote(t *testing.T) {
	a, state := credentialsAppState(t, fakec2patool.State{})
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: true})
	p, rid, _ := approvedWithRender(t, a, "Garden story", true)
	do[ops.CredentialsSetupOut](t, a, "credentials.setup", ops.CredentialsSetupIn{Test: true})
	do[ops.CredentialsStatusOut](t, a, "content.credentials.set", ops.ContentCredentialsSetIn{ProjectID: p.ID, Mode: models.CredentialsRequired})
	ready := func() *ops.ShipGateOut {
		return do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	}
	if g := ready(); !g.Ready {
		t.Fatalf("set up: %+v", g)
	}
	if err := fakec2patool.Save(state, fakec2patool.State{FailSign: "the certificate is invalid"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir}, person)
	if code(err) != "E_HELD" || !strings.Contains(err.Error(), "signing it failed") || !strings.Contains(err.Error(), "Nothing was written") {
		t.Fatalf("a failing signing: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("written anyway: %v", left)
	}
	g := ready()
	if g.Ready || len(g.Holds) != 1 || !strings.HasPrefix(g.Holds[0].Key, "credentials:failed:") || g.Holds[0].Paths[0].Op != "credentials.test" ||
		!strings.Contains(g.Holds[0].Says, "the last signing on this computer failed") {
		t.Fatalf("held for the failed signing: %+v", g)
	}
	sent := do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "Signing is down; the client takes it without.", Holds: shownKeys(g)})
	if !sent.Ready {
		t.Fatalf("sent anyway: %+v", sent)
	}
	out := do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if c := out.Files[0].ContentCredentials; c == nil || c.Signed || !strings.Contains(c.Problem, "with a note") || len(out.Files[0].SentAnyway) != 1 {
		t.Fatalf("sent without them: %+v", out.Files[0])
	}
	// It works again: a sample clears what failed, and the next delivery is signed.
	if err := fakec2patool.Save(state, fakec2patool.State{}); err != nil {
		t.Fatal(err)
	}
	if tried := do[ops.CredentialsTestOut](t, a, "credentials.test", struct{}{}); !tried.Verified {
		t.Fatalf("the sample: %+v", tried)
	}
	if g := ready(); holdOf(g, models.HoldCredentials) {
		t.Fatalf("still held: %+v", g)
	}
	out = do[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()})
	if c := out.Files[0].ContentCredentials; c == nil || !c.Signed || !c.Verified {
		t.Fatalf("signed again: %+v", c)
	}
}

// Two deliveries of one version into one folder (D436): each signs in a
// private folder of its own, so neither can place the other's copy; one
// goes out, the other is refused, and what is there is exactly the copy
// that delivery checked and recorded.
func TestTwoDeliveriesIntoOneFolderNeverSwapBytes(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	ctx := context.Background()
	_, rid, _ := approvedWithRender(t, a, "Garden story", false)
	do[ops.CredentialsSetupOut](t, a, "credentials.setup", ops.CredentialsSetupIn{Test: true})
	do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn})
	dir := t.TempDir()
	type result struct {
		out *ops.DeliverOut
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir}, person)
			results <- result{out, err}
		}()
	}
	delivered := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			if code(r.err) != "E_CONFLICT" {
				t.Fatalf("the other delivery: %v", r.err)
			}
			continue
		}
		delivered++
		f := r.out.Files[0]
		c := f.ContentCredentials
		b, err := os.ReadFile(f.Path)
		sum := sha256.Sum256(b)
		if err != nil || c == nil || !c.Signed || hex.EncodeToString(sum[:]) != c.SHA256 || f.SHA256 != c.SHA256 {
			t.Fatalf("the placed copy isn't the one recorded: %v %+v", err, c)
		}
		if h, err := c2pa.ManifestHash(f.Path); err != nil || h != c.ManifestHash {
			t.Fatalf("its manifest: %s %v, recorded %s", h, err, c.ManifestHash)
		}
	}
	if delivered != 1 {
		t.Fatalf("%d deliveries went out", delivered)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Fatalf("hidden files left: %v", left)
	}
}

// Moving a project to settings that ask less of Content Credentials is
// loosening (D418): an owner's, as a person — like a gate turned off.
func TestFollowingSettingsThatAskLessIsAnOwnersAsAPerson(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	strict, loose := project(t, a, "Strict client"), project(t, a, "Loose client")
	for _, p := range []models.Project{strict, loose} {
		do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: p.ID})
	}
	do[ops.CredentialsStatusOut](t, a, "content.credentials.set", ops.ContentCredentialsSetIn{ProjectID: strict.ID, Mode: models.CredentialsOn})
	do[ops.CredentialsStatusOut](t, a, "content.credentials.set", ops.ContentCredentialsSetIn{ProjectID: loose.ID, Mode: models.CredentialsOff})
	err := doErr(a, "content.follow", ops.ContentFollowIn{ProjectID: strict.ID, Follow: loose.ID}, ops.Caller{})
	if code(err) != "E_HUMAN_ONLY" || !strings.Contains(err.Error(), "Content Credentials") {
		t.Fatalf("an agent moved it to settings without them: %v", err)
	}
	member := ops.OverHTTP(context.Background(), func() bool { return false })
	b, _ := json.Marshal(ops.ContentFollowIn{ProjectID: strict.ID, Follow: loose.ID})
	if _, err := ops.Call(member, a, "content.follow", b, ops.Caller{Human: true, Remote: true}); code(err) != "E_UNAUTHORIZED" {
		t.Fatalf("a member: %v", err)
	}
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: strict.ID, Follow: loose.ID})
	if mode, _ := ops.CredentialsFor(context.Background(), a, strict.ID); mode != models.CredentialsOff {
		t.Fatalf("followed: %s", mode)
	}
}

func holdOf(g *ops.ShipGateOut, kind string) bool {
	for _, h := range g.Holds {
		if h.Kind == kind && !h.Covered {
			return true
		}
	}
	return false
}

func mustRevision(t *testing.T, a *app.App, id string) *models.Revision {
	t.Helper()
	var r models.Revision
	if err := a.DB.GetByID(context.Background(), store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// The workspace's choice is an owner's, and asking less is a person's.
func TestCredentialsChoiceIsAnOwners(t *testing.T) {
	a, _ := credentialsApp(t, fakec2patool.State{})
	ctx := context.Background()
	member := ops.OverHTTP(ctx, func() bool { return false })
	b, _ := json.Marshal(ops.CredentialsSetIn{Mode: models.CredentialsOn})
	if _, err := ops.Call(member, a, "credentials.set", b, ops.Caller{Human: true, Remote: true}); code(err) != "E_UNAUTHORIZED" {
		t.Fatalf("a member: %v", err)
	}
	do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsRequired})
	if err := doErr(a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent asked less: %v", err)
	}
	// A timestamp service: public https names only (D438), a person's choice.
	was := c2pa.LookupHost
	t.Cleanup(func() { c2pa.LookupHost = was })
	c2pa.LookupHost = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "timestamp.example.org":
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
		case "inside.example.org":
			return []net.IPAddr{{IP: net.ParseIP("203.0.113.11")}, {IP: net.ParseIP("10.0.0.5")}}, nil
		}
		return nil, errors.New("no such host")
	}
	for _, bad := range []string{"ftp://timestamps.example", "http://timestamp.example.org", "https://127.0.0.1/tsr", "https://[::1]/tsr", "https://localhost/tsr",
		"https://tsa.local/tsr", "https://intranet/tsr", "https://inside.example.org/tsr", "https://nowhere.example.org/tsr"} {
		if err := doErr(a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsRequired, TimestampURL: &bad}, person); code(err) != "E_INVALID_INPUT" {
			t.Errorf("the timestamp address %s: %v", bad, err)
		}
	}
	good := "https://timestamp.example.org/tsr"
	if err := doErr(a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsRequired, TimestampURL: &good}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent chose a timestamp service: %v", err)
	}
	st := do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsRequired, TimestampURL: &good})
	if st.Mode != models.CredentialsRequired || st.Workspace == nil || st.Workspace.TimestampURL != good || st.Workspace.UpdatedBy.UserName == "" {
		t.Fatalf("set: %+v", st)
	}
	if st := do[ops.CredentialsStatusOut](t, a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOff}); st.Mode != models.CredentialsOff {
		t.Fatalf("off: %+v", st)
	}
}
