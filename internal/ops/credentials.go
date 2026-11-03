package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// Content Credentials (D415–D424, docs/content-credentials.md): when MetaRush
// delivers a final video it can put a C2PA manifest into it, signed with this
// computer's signing certificate, saying how the video was made — with
// MetaRush, from which footage (by hash) and which parts AI made or changed.
// Off unless an owner turns it on (per workspace, and per Content Settings
// for a client); "required" makes the ship gate hold a version this computer
// can't sign. The certificate is set up by an owner, a person, on each
// computer that delivers: its key stays in this computer's keychain and never
// leaves MetaRush's process (c2patool asks MetaRush to sign).

// credentialsMeans says what each choice does, in plain words.
var credentialsMeans = map[string]string{
	models.CredentialsOff:      "Delivered videos go out without Content Credentials.",
	models.CredentialsOn:       "Every final video MetaRush delivers carries Content Credentials, signed on the computer that delivers it; one that can't be signed goes out without them, and its record says why.",
	models.CredentialsRequired: "Every final video MetaRush delivers carries Content Credentials, and a version this computer can't sign waits; a person can still send it out without them, with a note.",
}

// CredentialsFor says whether a project's delivered videos carry Content
// Credentials (off, on or required) and whose choice that is: the Content
// Settings it uses when they say, else the workspace's (off when nobody chose).
func CredentialsFor(ctx context.Context, a *app.App, projectID string) (mode, whose string) {
	if projectID != "" {
		if g, r, err := content.Resolve(ctx, a, projectID); err == nil && r.Owner != "" {
			if rec, err := content.Record(ctx, a, r.Owner); err == nil && rec != nil && rec.Credentials != nil {
				if r.Owner == projectID {
					return rec.Credentials.Mode, "this project's settings"
				}
				return rec.Credentials.Mode, "“" + g.Name(r.Owner) + "”'s settings"
			}
		}
	}
	ws := workspaceCredentials(ctx, a)
	if ws == nil || ws.Mode == "" {
		return models.CredentialsOff, "the workspace's choice"
	}
	return ws.Mode, "the workspace's choice"
}

func workspaceCredentials(ctx context.Context, a *app.App) *models.ContentCredentialsSettings {
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil {
		return nil
	}
	return ws.ContentCredentials
}

// CredentialsHere is this computer's part: whether it can sign, and with what.
type CredentialsHere struct {
	Machine string `json:"machine"`
	// Certificate is the signing certificate set up here (nil: none). Never its key.
	Certificate *models.SigningCertificate `json:"certificate,omitempty"`
	SetAt       *time.Time                 `json:"setAt,omitempty"`
	SetBy       string                     `json:"setBy,omitempty"`
	// Kept says where the key is: this computer's keychain, or its secrets file.
	Kept string `json:"kept,omitempty" jsonschema:"enum=,enum=keychain,enum=file"`
	// Tool is c2patool's version here ("" not here).
	Tool string `json:"tool,omitempty"`
	// Ready: this computer can sign now; Problems say what stops it.
	Ready    bool     `json:"ready"`
	Problems []string `json:"problems"`
	Says     string   `json:"says"`
}

// credentialsHere looks at this computer without reading the key.
func credentialsHere(ctx context.Context, a *app.App) CredentialsHere {
	h := CredentialsHere{Machine: a.Cfg.Installation.Name, Problems: []string{}}
	if a.Tools != nil && a.Tools.Has(tools.C2PATool) {
		h.Tool = a.Tools.Version(ctx, tools.C2PATool)
	}
	sv, err := c2pa.Public(a.Cfg)
	switch {
	case err != nil:
		h.Problems = append(h.Problems, apperr.From(err).Message)
	case sv == nil:
		h.Problems = append(h.Problems, "No signing certificate is set up on this computer.")
	default:
		cert := sv.Certificate
		at := sv.SetAt
		h.Certificate, h.SetAt, h.SetBy, h.Kept = &cert, &at, sv.SetBy, sv.Kept
		h.Problems = append(h.Problems, sv.Problems...)
	}
	if why := (c2pa.Tool{Tools: a.Tools}).Ready(ctx); why != "" {
		h.Problems = append(h.Problems, sentenceStart(why)+".")
	}
	h.Ready = len(h.Problems) == 0
	switch {
	case h.Ready && h.Certificate.Test:
		h.Says = fmt.Sprintf("This computer signs with a test certificate (until %s): verifiers show its signer as unknown, so it is for trying Content Credentials, not for a client.", h.Certificate.ValidUntil.Format("2 Jan 2006"))
	case h.Ready:
		h.Says = fmt.Sprintf("This computer signs as “%s” (issued by %s, until %s).", h.Certificate.Signer, orWords(h.Certificate.Issuer, "an authority"), h.Certificate.ValidUntil.Format("2 Jan 2006"))
	default:
		h.Says = "This computer can't sign Content Credentials yet: " + lowerFirst(strings.TrimSuffix(h.Problems[0], "."))
		if len(h.Problems) > 1 {
			h.Says += fmt.Sprintf(" (and %s more)", pluralWord(len(h.Problems)-1, "thing"))
		}
		h.Says += "."
	}
	return h
}

func orWords(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return "“" + s + "”"
}

// ---- status ----

// CredentialsStatusIn asks about Content Credentials, for a project or the workspace.
type CredentialsStatusIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project: also what its Content Settings say."`
}

// CredentialsStatusOut is the choice, whose it is, and this computer's part.
type CredentialsStatusOut struct {
	// Workspace is the workspace's choice (nil: nobody chose; off).
	Workspace *models.ContentCredentialsSettings `json:"workspace,omitempty"`
	// Settings is what the project's Content Settings say (nil: the workspace's), Of whose.
	Settings *models.CredentialsSetting `json:"settings,omitempty"`
	Of       string                     `json:"of,omitempty"`
	// Mode is what applies (to the project, else the workspace); Means says what it does.
	Mode  string `json:"mode" jsonschema:"enum=off,enum=on,enum=required"`
	Means string `json:"means"`
	Whose string `json:"whose"`
	// GateOn: the ship gate is on here, so "required" holds what can't be signed.
	GateOn  bool            `json:"gateOn"`
	Here    CredentialsHere `json:"here"`
	Summary string          `json:"summary"`
	Lines   []string        `json:"lines"`
}

func credentialsStatus(ctx context.Context, a *app.App, projectID string) (*CredentialsStatusOut, error) {
	out := &CredentialsStatusOut{Workspace: workspaceCredentials(ctx, a), Lines: []string{}}
	if projectID != "" {
		g, r, err := content.Resolve(ctx, a, projectID)
		if err != nil {
			return nil, err
		}
		if r.Owner != "" {
			if rec, err := content.Record(ctx, a, r.Owner); err == nil && rec != nil {
				out.Settings = rec.Credentials
			}
			out.Of = g.Name(r.Owner)
		}
	}
	out.Mode, out.Whose = CredentialsFor(ctx, a, projectID)
	out.Means = credentialsMeans[out.Mode]
	out.GateOn, _, _ = GateFor(ctx, a, projectID)
	out.Here = credentialsHere(ctx, a)
	switch out.Mode {
	case models.CredentialsOff:
		out.Summary = "Content Credentials are off (" + out.Whose + "): " + lowerFirst(out.Means)
	case models.CredentialsOn:
		out.Summary = "Content Credentials are on (" + out.Whose + "). " + out.Here.Says
	default:
		out.Summary = "Content Credentials are required (" + out.Whose + "). " + out.Here.Says
		if !out.GateOn {
			out.Summary += " Versions only wait for them while the ship gate is on (Settings → General); now a delivery that can't be signed goes out without them and says so."
		}
	}
	out.Lines = append(out.Lines, out.Summary)
	if out.Mode != models.CredentialsOff && !out.Here.Ready {
		out.Lines = append(out.Lines, "Set them up on this computer: Settings → Delivery, or metarush credentials setup.")
	}
	return out, nil
}

// ---- setting them up on this computer ----

// CredentialsSetupIn sets up this computer's signing certificate (owners,
// people only): a test certificate made here, or a certificate chain and its
// private key a person brings (PEM text, read once: the key goes into this
// computer's keychain and is never shown again).
type CredentialsSetupIn struct {
	// Test makes a test certificate here: verifiers show its signer as unknown.
	Test bool `json:"test,omitempty"`
	// Organisation is the name a test certificate says it is for ("" none: no one's name goes in).
	Organisation string `json:"organisation,omitempty" jsonschema:"maxLength=64"`
	// Certificate is the certificate chain (PEM): the signing certificate first.
	Certificate string `json:"certificate,omitempty" jsonschema:"maxLength=65536"`
	// PrivateKey is the signing certificate's private key (PEM, without a password).
	PrivateKey string `json:"privateKey,omitempty" jsonschema:"maxLength=65536"`
	// Replace sets it up over the certificate this computer has.
	Replace bool `json:"replace,omitempty"`
}

// CredentialsSetupOut is what was set up, and the sample signed with it.
type CredentialsSetupOut struct {
	Status  CredentialsStatusOut `json:"status"`
	Summary string               `json:"summary"`
	// Tried is a sample signed and read back with it (nil: c2patool isn't here yet).
	Tried *CredentialsTestOut `json:"tried,omitempty"`
}

func setupCredentials(ctx context.Context, a *app.App, in *CredentialsSetupIn) (*CredentialsSetupOut, error) {
	certPEM, keyPEM := strings.TrimSpace(in.Certificate), strings.TrimSpace(in.PrivateKey)
	in.PrivateKey = ""
	if in.Test == (certPEM != "" || keyPEM != "") {
		return nil, invalid("Either make a test certificate, or give a certificate file and its private key.")
	}
	if !in.Test && (certPEM == "" || keyPEM == "") {
		return nil, invalid("Give both the certificate file and its private key.")
	}
	if sv, err := c2pa.Public(a.Cfg); err == nil && sv != nil && !in.Replace {
		return nil, apperr.New(apperr.Conflict, "This computer already signs as “%s”. Replace it?", sv.Certificate.Signer).
			WithHint("Set it up again and say to replace it; the old key is removed from this computer.").WithCommand("metarush credentials setup --replace")
	}
	var cred *c2pa.Credential
	var err error
	if in.Test {
		cred, err = c2pa.MakeTest(in.Organisation, time.Now())
	} else {
		cred, err = c2pa.Parse([]byte(certPEM), []byte(keyPEM))
	}
	keyPEM = ""
	if err != nil {
		return nil, err
	}
	if p := cred.Problems(time.Now()); len(p) > 0 {
		return nil, apperr.New(apperr.InvalidInput, "That certificate can't sign Content Credentials: %s", lowerFirst(strings.Join(p, " "))).
			WithDetails(map[string]any{"problems": p})
	}
	who := a.Actor.UserName
	if _, err := c2pa.Save(a.Cfg, cred, who, time.Now()); err != nil {
		return nil, err
	}
	f := cred.Facts()
	a.Audit(ctx, "credentials.setup", "workspace", a.WorkspaceID(), "", map[string]any{"machine": a.Cfg.Installation.Name, "test": f.Test,
		"fingerprint": f.Fingerprint, "signer": f.Signer, "alg": f.Alg, "validUntil": f.ValidUntil})
	out := &CredentialsSetupOut{}
	if (c2pa.Tool{Tools: a.Tools}).Ready(ctx) == "" {
		if tried, err := tryCredentials(ctx, a); err == nil {
			out.Tried = tried
		}
	}
	st, err := credentialsStatus(ctx, a, "")
	if err != nil {
		return nil, err
	}
	out.Status = *st
	switch {
	case f.Test:
		out.Summary = "This computer now signs with a test certificate. Verifiers show its signer as unknown: it is for trying Content Credentials, not for a client."
	default:
		out.Summary = fmt.Sprintf("This computer now signs as “%s”. Its key is in this computer's keychain and is never shown again.", f.Signer)
	}
	switch {
	case out.Tried != nil && out.Tried.Verified:
		out.Summary += " A sample was signed and checked."
	case out.Tried != nil:
		out.Summary += " A sample was signed, but it didn't check out: " + out.Tried.Problem + "."
	default:
		out.Summary += " c2patool, which signs videos, isn't on this computer yet: add it in Settings → Components."
	}
	if st.Mode == models.CredentialsOff {
		out.Summary += " Content Credentials are still off; turn them on in Settings → Delivery."
	}
	return out, nil
}

// ---- trying them on a sample ----

// CredentialsTestOut is a sample signed and read back with this computer's certificate.
type CredentialsTestOut struct {
	Verified bool `json:"verified"`
	// Signer is who the credentials name; Test: a test certificate.
	Signer       string `json:"signer"`
	Test         bool   `json:"test"`
	ManifestHash string `json:"manifestHash,omitempty"`
	Problem      string `json:"problem,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Summary      string `json:"summary"`
}

// tryCredentials signs MetaRush's one-second sample (no one's footage) in a
// private folder and reads it back.
func tryCredentials(ctx context.Context, a *app.App) (*CredentialsTestOut, error) {
	cred, _, err := c2pa.Load(a.Cfg)
	if err != nil {
		if err == c2pa.ErrNotSetUp {
			return nil, apperr.New(apperr.NotConfigured, "No signing certificate is set up on this computer.").
				WithHint("An owner sets one up: Settings → Delivery, or in their own terminal.").WithCommand("metarush credentials setup")
		}
		return nil, err
	}
	tool := c2pa.Tool{Tools: a.Tools}
	if why := tool.Ready(ctx); why != "" {
		return nil, apperr.New(apperr.ToolMissing, "%s.", sentenceStart(why)).WithHint("Add it in Settings → Components.").WithCommand("metarush components add content-credentials")
	}
	dir, err := os.MkdirTemp("", "metarush-credentials-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "sample.mp4")
	if err := c2pa.WriteSample(in); err != nil {
		return nil, err
	}
	def, err := c2pa.Manifest{Version: version.Current().Version}.Definition()
	if err != nil {
		return nil, err
	}
	s, err := tool.Sign(ctx, c2pa.SignIn{Video: in, Out: filepath.Join(dir, "signed.mp4"), Manifest: def, Credential: cred, TimestampURL: timestampURL(ctx, a)})
	if err != nil {
		return nil, err
	}
	f := cred.Facts()
	out := &CredentialsTestOut{Verified: s.Verified, Signer: f.Signer, Test: f.Test, ManifestHash: s.ManifestHash, Problem: s.Problem, Tool: a.Tools.Version(ctx, tools.C2PATool)}
	if s.Verified {
		out.Summary = fmt.Sprintf("A sample was signed as “%s” and its Content Credentials check out.", f.Signer)
		if f.Test {
			out.Summary += " It is a test certificate: verifiers show the signer as unknown."
		}
	} else {
		out.Summary = "A sample was signed, but its Content Credentials don't check out: " + s.Problem + "."
	}
	return out, nil
}

// timestampURL is the timestamp service the workspace chose ("" none).
func timestampURL(ctx context.Context, a *app.App) string {
	if ws := workspaceCredentials(ctx, a); ws != nil {
		return ws.TimestampURL
	}
	return ""
}

// ---- checking a file ----

// CredentialsCheckIn names a video on this computer.
type CredentialsCheckIn struct {
	Path string `json:"path" jsonschema:"description=A video file on this computer (a delivered one, say)."`
}

// CredentialsCheckOut says whether a file carries Content Credentials that check out.
type CredentialsCheckOut struct {
	Carries  bool   `json:"carries"`
	Verified bool   `json:"verified"`
	Problem  string `json:"problem,omitempty"`
	// Signer is whom the signature's certificate names (c2patool's words).
	Signer       string   `json:"signer,omitempty"`
	Generator    string   `json:"generator,omitempty"`
	ManifestHash string   `json:"manifestHash,omitempty"`
	Parts        int      `json:"parts"`
	SourceTypes  []string `json:"sourceTypes,omitempty"`
	// Ours: signed with this computer's certificate.
	Ours    bool   `json:"ours"`
	Summary string `json:"summary"`
}

func checkCredentials(ctx context.Context, a *app.App, in *CredentialsCheckIn) (*CredentialsCheckOut, error) {
	p, err := filepath.Abs(strings.TrimSpace(in.Path))
	if err != nil || strings.TrimSpace(in.Path) == "" {
		return nil, invalid("Say which video to check.")
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return nil, apperr.New(apperr.NotFound, "There's no video at that path.").WithDetails(map[string]any{"path": p})
	}
	tool := c2pa.Tool{Tools: a.Tools}
	if why := tool.Ready(ctx); why != "" {
		return nil, apperr.New(apperr.ToolMissing, "%s.", sentenceStart(why)).WithHint("Add it in Settings → Components.").WithCommand("metarush components add content-credentials")
	}
	r, err := tool.Read(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &CredentialsCheckOut{}
	if r.None {
		out.Summary = "This video carries no Content Credentials."
		return out, nil
	}
	out.Carries, out.Generator, out.Parts, out.SourceTypes = true, r.Generator, r.Ingredients, r.SourceTypes
	out.Signer = strings.TrimSpace(orEmpty(r.Signer.Issuer, r.Signer.CommonName))
	out.ManifestHash, _ = c2pa.ManifestHash(p)
	if sv, err := c2pa.Public(a.Cfg); err == nil && sv != nil {
		out.Ours = r.Signer.Serial == sv.Certificate.Serial
	}
	out.Verified, out.Problem = r.Check(nil)
	switch {
	case out.Verified && out.Ours:
		out.Summary = fmt.Sprintf("This video carries Content Credentials that check out, signed with this computer's certificate (%s).", out.Signer)
	case out.Verified:
		out.Summary = fmt.Sprintf("This video carries Content Credentials that check out, signed by %s.", orWords(out.Signer, "someone this computer doesn't know"))
	default:
		out.Summary = "This video carries Content Credentials, but " + out.Problem + "."
	}
	return out, nil
}

func orEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---- the choices ----

// CredentialsSetIn is the workspace's choice (owners).
type CredentialsSetIn struct {
	Mode string `json:"mode" jsonschema:"enum=off,enum=on,enum=required,description=off: delivered videos carry none; on: every final delivery is signed; required: and a version this computer can't sign waits."`
	// TimestampURL is a timestamp service (http or https) that vouches for
	// when each video was signed; nil leaves it as it is, "" takes it away.
	TimestampURL *string `json:"timestampUrl,omitempty" jsonschema:"maxLength=200"`
}

// ContentCredentialsSetIn is a Content Settings' word (owners).
type ContentCredentialsSetIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project with its own settings (default: the default's)."`
	Mode      string `json:"mode" jsonschema:"enum=on,enum=off,enum=required,enum=workspace,description=on, required or off for every project using these settings; workspace: the workspace's choice."`
}

// credentialsRank orders the choices by how much they ask: taking one back
// is a person's (D418).
var credentialsRank = map[string]int{models.CredentialsOff: 0, "": 0, models.CredentialsOn: 1, models.CredentialsRequired: 2}

func setWorkspaceCredentials(ctx context.Context, a *app.App, in *CredentialsSetIn) (*CredentialsStatusOut, error) {
	if _, ok := credentialsMeans[in.Mode]; !ok {
		return nil, invalid("Say off, on or required.")
	}
	old := workspaceCredentials(ctx, a)
	cur := models.CredentialsOff
	if old != nil {
		cur = old.Mode
	}
	if credentialsRank[in.Mode] < credentialsRank[cur] && !callerOf(ctx).Human {
		return nil, apperr.New(apperr.HumanOnly, "Only a person can ask less of Content Credentials than now.").
			WithHint("Change it yourself in Settings → Delivery, or in your own terminal.").WithCommand("metarush credentials " + in.Mode)
	}
	set := models.ContentCredentialsSettings{Mode: in.Mode, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}
	if old != nil {
		set.TimestampURL = old.TimestampURL
	}
	if in.TimestampURL != nil {
		u := strings.TrimSpace(*in.TimestampURL)
		if u != "" {
			if why := c2pa.TimestampProblem(u); why != "" {
				return nil, invalid("%s", why)
			}
		} else if set.TimestampURL != "" && !callerOf(ctx).Human {
			return nil, apperr.New(apperr.HumanOnly, "Only a person can stop timestamping Content Credentials.").WithCommand("metarush credentials " + in.Mode + " --no-timestamp")
		}
		set.TimestampURL = u
	}
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"contentCredentials": set}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "credentials.set", "workspace", a.WorkspaceID(), "", map[string]any{"mode": in.Mode, "timestamp": set.TimestampURL != ""})
	return credentialsStatus(ctx, a, "")
}

// ---- the ship gate (D418) ----

// credentialsHold holds a version whose final delivery must carry Content
// Credentials while this computer can't sign them (nil: nothing holds it).
// A person may send it out without them, with a note.
func credentialsHold(ctx context.Context, a *app.App, p *models.Project) *GateHold {
	if mode, _ := CredentialsFor(ctx, a, p.ID); mode != models.CredentialsRequired {
		return nil
	}
	h := credentialsHere(ctx, a)
	if h.Ready {
		return nil
	}
	// A stable key per reason, so a note covers only what the person saw.
	key := "credentials:none"
	if sv, err := c2pa.Public(a.Cfg); err == nil && sv != nil {
		key = "credentials:tool"
		if len(sv.Problems) > 0 {
			key = "credentials:certificate:" + sv.Certificate.Fingerprint[:12]
		}
	}
	return &GateHold{ShipHold: models.ShipHold{Key: key, Kind: models.HoldCredentials,
		Says: "Its videos must carry Content Credentials, and this computer can't sign them: " + lowerFirst(strings.TrimSuffix(h.Problems[0], ".")) + "."},
		Paths: []GatePath{{Label: "Set them up", Link: links.Credentials(), Command: "metarush credentials setup", People: true}}}
}

func init() {
	Def("credentials.status", "Content Credentials: whether delivered videos carry them (the workspace's choice, and what a project's Content Settings say) and whether this computer can sign them, with its certificate (never its key)", Flags{},
		func(ctx context.Context, a *app.App, in *CredentialsStatusIn) (*CredentialsStatusOut, error) {
			pid := ""
			if strings.TrimSpace(in.ProjectID) != "" {
				p, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				pid = p.ID
			}
			return credentialsStatus(ctx, a, pid)
		})
	Def("credentials.setup", "Owners, people only: set up this computer's signing certificate for Content Credentials — a test certificate made here, or a certificate chain and its private key, read once (the key goes into this computer's keychain and is never shown again)", Flags{Mutating: true, OwnerOnly: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *CredentialsSetupIn) (*CredentialsSetupOut, error) {
			return setupCredentials(ctx, a, in)
		})
	Def("credentials.test", "Sign MetaRush's one-second sample with this computer's certificate and read it back: whether Content Credentials work here", Flags{},
		func(ctx context.Context, a *app.App, _ *struct{}) (*CredentialsTestOut, error) {
			return tryCredentials(ctx, a)
		})
	Def("credentials.check", "Read a video file's Content Credentials on this computer: whether it carries them, whether they check out, who signed them and what they say", Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *CredentialsCheckIn) (*CredentialsCheckOut, error) {
			return checkCredentials(ctx, a, in)
		})
	Def("credentials.remove", "Owners, people only: remove this computer's signing certificate and its key (videos delivered here then go out without Content Credentials, or wait where they are required)", Flags{Mutating: true, OwnerOnly: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, _ *struct{}) (*CredentialsStatusOut, error) {
			had, err := c2pa.Remove(a.Cfg)
			if err != nil {
				return nil, err
			}
			if !had {
				return nil, apperr.New(apperr.NotFound, "This computer has no signing certificate to remove.")
			}
			a.Audit(ctx, "credentials.remove", "workspace", a.WorkspaceID(), "", map[string]any{"machine": a.Cfg.Installation.Name})
			return credentialsStatus(ctx, a, "")
		})
	Def("credentials.set", "Owners: whether delivered videos carry Content Credentials — off, on (every final delivery signed) or required (a version this computer can't sign waits) — and a timestamp service; asking less is people only", Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *CredentialsSetIn) (*CredentialsStatusOut, error) {
			return setWorkspaceCredentials(ctx, a, in)
		})
	Def("content.credentials.set", "Owners: say for a project's own Content Settings whether its delivered videos carry Content Credentials (on, required or off; asking less is people only), or follow the workspace", Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *ContentCredentialsSetIn) (*CredentialsStatusOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			before, _ := CredentialsFor(ctx, a, t.ProjectID)
			after := in.Mode
			upd := bson.M{}
			switch in.Mode {
			case models.CredentialsOn, models.CredentialsOff, models.CredentialsRequired:
				upd["$set"] = bson.M{"credentials": models.CredentialsSetting{Mode: in.Mode, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}}
			case "workspace":
				upd["$unset"] = bson.M{"credentials": ""}
				after = models.CredentialsOff
				if ws := workspaceCredentials(ctx, a); ws != nil {
					after = ws.Mode
				}
			default:
				return nil, invalid("Say on, required, off or workspace.")
			}
			if credentialsRank[after] < credentialsRank[before] && !callerOf(ctx).Human {
				return nil, apperr.New(apperr.HumanOnly, "Only a person can ask less of these videos' Content Credentials.").
					WithHint("Change it yourself in the project's Settings tab (Content Credentials), or in your own terminal.")
			}
			if err := content.Update(ctx, a, t.ProjectID, upd); err != nil {
				return nil, err
			}
			a.Audit(ctx, "content.credentials.set", "project", t.ProjectID, t.ProjectID, map[string]any{"mode": in.Mode})
			out, err := credentialsStatus(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
	RegisterReady("content-credentials", func(ctx context.Context, a *app.App) (bool, string) {
		if sv, err := c2pa.Public(a.Cfg); err == nil && sv != nil && len(sv.Problems) == 0 {
			return true, ""
		}
		return false, "a signing certificate, set up once by an owner on this computer"
	})
	// First-party abilities (D269): Content Credentials is turning them on;
	// every final delivery then carries them.
	Offer("credentials.set", Offering{ID: "content-credentials", Group: "deliver", Title: "Content Credentials",
		Does:  "Signs every final video MetaRush delivers with Content Credentials: made with MetaRush, from which footage, and which parts AI made or changed.",
		Input: map[string]any{"mode": models.CredentialsOn},
		Words: []string{"c2pa", "provenance", "signed video", "authenticity", "made with ai label", "content credentials"},
		Needs: Needs{Programs: []string{tools.C2PATool}, Setup: "content-credentials"}})
}
