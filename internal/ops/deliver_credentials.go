package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// Content Credentials in delivery (D418–D420): where they are on, every
// final video is signed before anything is written — c2patool writes a
// signed copy next to where the video goes, MetaRush reads it back, and only
// a copy whose credentials check out is placed. A video that can't be signed
// goes out without them and says why; where they are required, nothing is
// delivered instead (the ship gate held it already when this computer can't
// sign at all). Review copies are never signed.

// DeliveryCredentials is a delivered video's Content Credentials (D420), in
// its record (the .json next to it) and in what delivery answers.
type DeliveryCredentials struct {
	// Signed: the delivered file carries Content Credentials; Signer is whom
	// their certificate names.
	Signed bool   `json:"signed"`
	Signer string `json:"signer,omitempty"`
	// Verified: MetaRush read the delivered copy back and they checked out.
	Verified bool `json:"verified"`
	// ManifestHash is the SHA-256 of the manifest store in the file.
	ManifestHash string `json:"manifestHash,omitempty"`
	// TestCertificate: signed with a test certificate verifiers show as unknown.
	TestCertificate bool   `json:"testCertificate,omitempty"`
	Issuer          string `json:"issuer,omitempty"`
	// Fingerprint is the signing certificate's SHA-256.
	Fingerprint string `json:"fingerprint,omitempty"`
	Timestamped bool   `json:"timestamped,omitempty"`
	// SHA256 and Size are the delivered file's, credentials and all (the
	// render's are under render).
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// What the manifest says: the parts it names, of which a generative AI
	// model made or edited AIParts and AI changed ChangedParts otherwise, and
	// the video's own digital source type.
	Parts        int    `json:"parts,omitempty"`
	AIParts      int    `json:"aiParts,omitempty"`
	ChangedParts int    `json:"changedParts,omitempty"`
	SourceType   string `json:"sourceType,omitempty"`
	// Required: the settings ask every video to carry them.
	Required bool `json:"required,omitempty"`
	// Says it in plain words; Problem says why it carries none.
	Says    string `json:"says"`
	Problem string `json:"problem,omitempty"`
	// Tool is c2patool's version.
	Tool string `json:"tool,omitempty"`
}

// itemCredentials is one delivered video's signing.
type itemCredentials struct {
	rec *DeliveryCredentials
	// signed is the signed copy waiting in its private folder (nil none):
	// placed where the video goes, then checked again (placeSigned).
	signed *c2pa.Signed
}

// removeSigned throws away the signed copies still waiting.
func removeSigned(creds map[*deliveryItem]*itemCredentials) {
	for _, ic := range creds {
		if ic != nil && ic.signed != nil {
			ic.signed.Remove()
			ic.signed = nil
		}
	}
}

// signDeliveries signs every final video of a delivery that is to carry
// Content Credentials, each in a private folder of its own (D436). Where
// they are required and a signing fails, it throws away what it signed and
// refuses; a signing that fails is remembered on this computer until one
// works (D437), so the ship gate holds versions that require them and a
// person can send one out without them, with a note.
func signDeliveries(ctx context.Context, a *app.App, items []*deliveryItem, scs map[*deliveryItem]*DeliverySidecar, quality string, gates map[string]*ShipGateOut) (map[*deliveryItem]*itemCredentials, error) {
	out := map[*deliveryItem]*itemCredentials{}
	var cred *c2pa.Credential
	var credErr error
	loaded := false
	tool := c2pa.NewTool(a.Cfg, a.Tools)
	toolVersion, tsa := "", ""
	worked, failed := false, ""
	defer func() {
		switch {
		case failed != "":
			_ = c2pa.RecordFailure(a.Cfg, failed, time.Now())
		case worked:
			c2pa.ClearFailure(a.Cfg)
		}
	}()
	for _, it := range items {
		mode, _ := CredentialsFor(ctx, a, it.proj.ID)
		if mode == models.CredentialsOff {
			continue
		}
		rec := &DeliveryCredentials{Required: mode == models.CredentialsRequired}
		ic := &itemCredentials{rec: rec}
		out[it] = ic
		if quality != "final" {
			rec.Says = "Review copies don't carry Content Credentials; final videos do."
			continue
		}
		if g := gates[it.rev.ID]; g != nil {
			if coveredCredentialsHold(g) != nil {
				rec.Problem = "a person sent it out without them, with a note"
				rec.Says = "This video went out without Content Credentials: " + rec.Problem + "."
				continue
			}
		}
		if !loaded {
			loaded = true
			cred, _, credErr = c2pa.Load(a.Cfg)
			if credErr == nil {
				if p := cred.Problems(time.Now()); len(p) > 0 {
					credErr = apperr.New(apperr.NotConfigured, "%s", p[0])
				}
			}
			if credErr == nil {
				if why := tool.Ready(ctx); why != "" {
					credErr = apperr.New(apperr.ToolMissing, "%s.", sentenceStart(why))
				}
			}
			if credErr == nil {
				var why string
				if tsa, why = timestampFor(ctx, a); why != "" {
					credErr = apperr.New(apperr.NotConfigured, "%s", why)
				}
			}
			if credErr == nil {
				toolVersion = a.Tools.Version(ctx, tools.C2PATool)
			}
		}
		// fail: this video carries none, and says why; where they are
		// required nothing is delivered. runtime: a signing that was tried.
		fail := func(why string, runtime bool, err error) error {
			rec.Problem = why
			rec.Says = "This video went out without Content Credentials: " + why + "."
			if runtime {
				failed = why
			}
			if !rec.Required {
				return nil
			}
			removeSigned(out)
			e := apperr.New(apperr.Held, "Version %d of “%s” wasn't delivered: its videos must carry Content Credentials, and %s. Nothing was written.", it.rev.Number, it.proj.Name, why).
				WithHint("Try it on a sample (Settings → Delivery) and deliver again, or a person sends it out without them, with a note (Deliver anyway… on the version's page).").
				WithCommand("metarush credentials test")
			if err != nil {
				e = e.WithDetails(map[string]any{"error": apperr.From(err).Message, "anyway": fmt.Sprintf("metarush deliver override %s --version r%d --note \"…\"", it.proj.ID, it.rev.Number)})
			}
			return e
		}
		if credErr != nil {
			why := "this computer can't sign them: " + lowerFirst(strings.TrimSuffix(apperr.From(credErr).Message, "."))
			if errors.Is(credErr, c2pa.ErrNotSetUp) {
				why = "no signing certificate is set up on this computer"
			}
			if err := fail(why, false, credErr); err != nil {
				return nil, err
			}
			continue
		}
		o := it.render.Output
		src, err := a.Cache.Ensure(ctx, a.Media, o.StorageKey, o.SHA256, o.Size, nil)
		if err != nil {
			removeSigned(out)
			return nil, apperr.Wrap(apperr.Storage, err, "The %s couldn't be copied to this machine, so it wasn't exported.", strings.ToLower(models.RenderLabel(it.render.Quality, it.rev.Number))).
				WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"renderId": it.render.ID})
		}
		m := manifestFor(ctx, a, it, scs[it])
		def, err := m.Definition()
		if err != nil {
			removeSigned(out)
			return nil, err
		}
		s, err := tool.Sign(ctx, c2pa.SignIn{Video: src, Manifest: def, Credential: cred, TimestampURL: tsa})
		if err != nil {
			if err := fail("signing it failed: "+lowerFirst(strings.TrimSuffix(apperr.From(err).Message, ".")), true, err); err != nil {
				return nil, err
			}
			continue
		}
		if !s.Verified {
			s.Remove()
			rec.Verified = false
			if err := fail("its credentials didn't check out after signing ("+s.Problem+")", true, nil); err != nil {
				return nil, err
			}
			continue
		}
		worked = true
		f := cred.Facts()
		parts, ai, changed := m.Counts()
		rec.Signed, rec.Verified, rec.Signer, rec.Issuer, rec.TestCertificate = true, true, f.Signer, f.Issuer, f.Test
		rec.ManifestHash, rec.Fingerprint, rec.Timestamped, rec.SHA256, rec.Size = s.ManifestHash, f.Fingerprint, s.Timestamped, s.SHA256, s.Size
		rec.Parts, rec.AIParts, rec.ChangedParts, rec.SourceType, rec.Tool = parts, ai, changed, m.SourceType(), toolVersion
		rec.Says = credentialsSays(rec)
		ic.signed = s
	}
	return out, nil
}

// placeSigned puts a signed copy where the video goes — linked when the
// folders share a disk, else copied and hashed as it is written — never over
// a file (D436), then checks that what is there is the very copy that
// checked out, by its SHA-256 and its manifest's, before anything records it.
func placeSigned(s *c2pa.Signed, dst string) error {
	if err := os.Link(s.Path, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return placedOver(dst)
		}
		if _, e := os.Lstat(dst); e == nil {
			return placedOver(dst)
		}
		if err := copyVerified(s.Path, dst, s.SHA256, "the signed copy"); err != nil {
			return err
		}
	}
	sha, _ := media.HashFile(dst)
	mh, _ := c2pa.ManifestHash(dst)
	if sha != s.SHA256 || mh != s.ManifestHash {
		_ = os.Remove(dst) // made a moment ago, by this delivery
		return apperr.New(apperr.Integrity, "The signed copy of %s changed after MetaRush checked it, so it wasn't delivered.", filepath.Base(dst)).
			WithHint("Deliver it again.").WithDetails(map[string]any{"path": dst, "sha256": sha, "expected": s.SHA256})
	}
	return nil
}

func placedOver(dst string) error {
	return apperr.New(apperr.Conflict, "A file named %s appeared in that folder while exporting, and MetaRush never overwrites files.", filepath.Base(dst)).
		WithHint("Choose another folder, or move it and export again.").WithDetails(map[string]any{"path": dst})
}

// coveredCredentialsHold is the Content Credentials hold a person sent a
// version out past (nil: none).
func coveredCredentialsHold(g *ShipGateOut) *GateHold {
	for i := range g.Holds {
		if g.Holds[i].Kind == models.HoldCredentials && g.Holds[i].Covered {
			return &g.Holds[i]
		}
	}
	return nil
}

// credentialsSays says what a signed video carries.
func credentialsSays(r *DeliveryCredentials) string {
	who := "“" + r.Signer + "”"
	if r.TestCertificate {
		who = "a test certificate (verifiers show its signer as unknown)"
	}
	s := "This video carries Content Credentials, signed by " + who + " and checked: they say it was made with MetaRush"
	switch {
	case r.AIParts > 0:
		s += fmt.Sprintf(", from %s, %d of them made or edited with AI", pluralWord(r.Parts, "part"), r.AIParts)
	case r.Parts > 0:
		s += ", from " + pluralWord(r.Parts, "part")
	}
	if r.ChangedParts > 0 {
		s += fmt.Sprintf(" (%d changed by AI, nothing new made)", r.ChangedParts)
	}
	return s + "."
}

// credentialsMarkdown is the Content Credentials section of a delivered
// video's notes file.
func credentialsMarkdown(r *DeliveryCredentials) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Content Credentials\n\n")
	if !r.Signed {
		b.WriteString(r.Says + "\n")
		return b.String()
	}
	b.WriteString(r.Says + " They name its footage, sound and pictures by their fingerprints, never by name, and no names of people, projects or clients.\n\n")
	b.WriteString("Apps and sites that read Content Credentials show this. Upload the file as it is: editing or converting it takes them out.\n")
	if r.TestCertificate {
		b.WriteString("\nThey were signed with a test certificate, which is for trying Content Credentials: verifiers show the signer as unknown.\n")
	}
	return b.String()
}

// recordSigned appends a signed delivery to its version (the receipt lists
// them). It tries twice and returns the error (the caller says so).
func recordSigned(ctx context.Context, a *app.App, revisionID string, sd models.SignedDelivery) error {
	var err error
	for try := 0; try < 2; try++ {
		var res *mongo.UpdateResult
		res, err = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": revisionID, "workspaceId": a.WorkspaceID()}, bson.M{"$push": bson.M{"contentCredentials": sd}})
		if err == nil && res.MatchedCount == 0 {
			err = fmt.Errorf("version %s not found", revisionID)
		}
		if err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// manifestFor is what a delivered video's manifest says about it (D419,
// D435): every piece of AI work it plays and shows, its own and its nested
// cuts', with what AI did, then everything else, by hash and kind only.
func manifestFor(ctx context.Context, a *app.App, it *deliveryItem, sc *DeliverySidecar) c2pa.Manifest {
	uses, _, pieces := it.ai(ctx, a)
	return manifestFrom(version.Current().Version, uses, pieces, sc)
}

// providerTitle is an AI service's name as people know it ("" none).
func providerTitle(name string) string {
	switch name {
	case "":
		return ""
	case "elevenlabs":
		return "ElevenLabs"
	case "local":
		return "this computer"
	}
	return name
}

// credentialsApply reports whether a delivered video's notes say something
// about Content Credentials: a final video whose settings turn them on.
func credentialsApply(ctx context.Context, a *app.App, it *deliveryItem, quality string) bool {
	if quality != "final" {
		return false
	}
	mode, _ := CredentialsFor(ctx, a, it.proj.ID)
	return mode != models.CredentialsOff
}

// credentialsNote says, for the person delivering, what Content Credentials
// the videos carry ("" when none were asked for).
func credentialsNote(files []DeliverFile) string {
	var signed, test, without []DeliverFile
	for _, f := range files {
		switch c := f.ContentCredentials; {
		case c == nil:
		case c.Signed:
			signed = append(signed, f)
			if c.TestCertificate {
				test = append(test, f)
			}
		case c.Problem != "":
			without = append(without, f)
		}
	}
	var out string
	if len(signed) > 0 {
		c := signed[0].ContentCredentials
		who := "“" + c.Signer + "”"
		if len(test) == len(signed) {
			who = "a test certificate (verifiers show its signer as unknown: it is for trying them, not for a client)"
		}
		switch {
		case len(files) == 1:
			out = " This video carries Content Credentials, signed by " + who + " and checked; its .notes.md file says so."
		case len(signed) == len(files):
			out = " These videos carry Content Credentials, signed by " + who + " and checked; their .notes.md files say so."
		default:
			out = fmt.Sprintf(" %d of these videos carry Content Credentials, signed by %s and checked; the .notes.md files say which.", len(signed), who)
		}
	}
	for _, f := range without {
		out += fmt.Sprintf(" %s went out without Content Credentials: %s.", filepath.Base(f.Path), f.ContentCredentials.Problem)
	}
	return out
}
