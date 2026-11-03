package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/editspec"
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
	// tmp is the signed copy waiting next to where the video goes ("" none).
	tmp string
}

// signDeliveries signs every final video of a delivery that is to carry
// Content Credentials, into hidden copies next to where each goes. Where they
// are required and a signing fails, it removes what it wrote and refuses.
func signDeliveries(ctx context.Context, a *app.App, items []*deliveryItem, scs map[*deliveryItem]*DeliverySidecar, dir, quality string, gates map[string]*ShipGateOut) (map[*deliveryItem]*itemCredentials, error) {
	out := map[*deliveryItem]*itemCredentials{}
	var cred *c2pa.Credential
	var credErr error
	loaded := false
	tool := c2pa.Tool{Tools: a.Tools}
	toolVersion := ""
	cleanup := func() {
		for _, ic := range out {
			if ic.tmp != "" {
				_ = os.Remove(ic.tmp)
			}
		}
	}
	tsa := timestampURL(ctx, a)
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
				toolVersion = a.Tools.Version(ctx, tools.C2PATool)
			}
		}
		fail := func(why string, err error) error {
			rec.Problem = why
			rec.Says = "This video went out without Content Credentials: " + why + "."
			if !rec.Required {
				return nil
			}
			cleanup()
			e := apperr.New(apperr.Held, "Version %d of “%s” wasn't delivered: its videos must carry Content Credentials, and %s. Nothing was written.", it.rev.Number, it.proj.Name, why).
				WithHint("Fix it on this computer and deliver again (Settings → Delivery tries a sample), or a person sends it out without them, with a note.").
				WithCommand("metarush credentials test")
			if err != nil {
				e = e.WithDetails(map[string]any{"error": apperr.From(err).Message})
			}
			return e
		}
		if credErr != nil {
			why := "this computer can't sign them: " + lowerFirst(strings.TrimSuffix(apperr.From(credErr).Message, "."))
			if errors.Is(credErr, c2pa.ErrNotSetUp) {
				why = "no signing certificate is set up on this computer"
			}
			if err := fail(why, credErr); err != nil {
				return nil, err
			}
			continue
		}
		o := it.render.Output
		src, err := a.Cache.Ensure(ctx, a.Media, o.StorageKey, o.SHA256, o.Size, nil)
		if err != nil {
			cleanup()
			return nil, apperr.Wrap(apperr.Storage, err, "The %s couldn't be copied to this machine, so it wasn't exported.", strings.ToLower(models.RenderLabel(it.render.Quality, it.rev.Number))).
				WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"renderId": it.render.ID})
		}
		m := manifestFor(ctx, a, it, scs[it])
		def, err := m.Definition()
		if err != nil {
			cleanup()
			return nil, err
		}
		tmp := filepath.Join(dir, "."+it.base+".signing.mp4")
		_ = os.Remove(tmp) // a copy an earlier delivery left when it stopped
		s, err := tool.Sign(ctx, c2pa.SignIn{Video: src, Out: tmp, Manifest: def, Credential: cred, TimestampURL: tsa})
		if err != nil {
			if err := fail("signing it failed: "+lowerFirst(strings.TrimSuffix(apperr.From(err).Message, ".")), err); err != nil {
				return nil, err
			}
			continue
		}
		if !s.Verified {
			_ = os.Remove(tmp)
			why := "its credentials didn't check out after signing (" + s.Problem + ")"
			rec.Verified = false
			if err := fail(why, nil); err != nil {
				return nil, err
			}
			continue
		}
		f := cred.Facts()
		parts, ai, changed := m.Counts()
		rec.Signed, rec.Verified, rec.Signer, rec.Issuer, rec.TestCertificate = true, true, f.Signer, f.Issuer, f.Test
		rec.ManifestHash, rec.Fingerprint, rec.Timestamped, rec.SHA256, rec.Size = s.ManifestHash, f.Fingerprint, s.Timestamped, s.SHA256, s.Size
		rec.Parts, rec.AIParts, rec.ChangedParts, rec.SourceType, rec.Tool = parts, ai, changed, m.SourceType(), toolVersion
		rec.Says = credentialsSays(rec)
		ic.tmp = tmp
	}
	return out, nil
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

// manifestFor is what a delivered video's manifest says about it: the parts
// its record lists, the AI ones first (a part listed twice keeps what AI did
// to it), by hash and kind only.
func manifestFor(ctx context.Context, a *app.App, it *deliveryItem, sc *DeliverySidecar) c2pa.Manifest {
	m := c2pa.Manifest{Version: version.Current().Version}
	format := func(id string) string {
		if as, ok := it.assets[id]; ok {
			return as.MIME
		}
		if it.extras != nil {
			if as, ok := it.extras.Asset(id); ok && as != nil {
				return as.MIME
			}
		}
		return ""
	}
	if sc != nil && sc.Generated != nil {
		for _, c := range sc.Generated.Clips {
			p := c2pa.Part{SHA256: c.SHA256, Format: format(c.AssetID), AI: c2pa.AIMade, Made: strings.Trim(strings.Join([]string{c.Provider, c.Model}, ", "), ", ")}
			switch c.Kind {
			case "", models.GenKindVideo:
				p.What = "footage"
			case "picture":
				p.What = "picture"
				if c.Edited {
					p.AI = c2pa.AIEnhanced
				}
			case "voice":
				p.What = "voice"
				for _, u := range c.UsedAs {
					if strings.HasPrefix(u, "said:") {
						p.What = "words"
					}
				}
			case "lip-sync", "voice-change":
				p.What, p.AI = c.Kind, c2pa.AIEdited
			default:
				p.What = c.Kind
			}
			m.Parts = append(m.Parts, p)
		}
	}
	// The narration: each line's recording (an AI voice's made by AI) and a
	// cleaned-up copy it plays; and the clearer copies of voices.
	narration := func(assetID string, ai bool, provider string, processed *editspec.ProcessedRef, useProcessed bool) {
		if as, ok := it.assets[assetID]; ok {
			p := c2pa.Part{What: "sound", SHA256: as.SHA256, Format: as.MIME}
			if ai {
				p.What, p.AI, p.Made = "voice", c2pa.AIMade, providerTitle(provider)
			}
			m.Parts = append(m.Parts, p)
		}
		if useProcessed && processed != nil {
			if as, ok := it.assets[processed.AssetID]; ok {
				m.Parts = append(m.Parts, c2pa.Part{What: "sound", SHA256: as.SHA256, Format: as.MIME, AI: c2pa.AIEnhanced})
			}
		}
	}
	if v := it.rev.Spec.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				narration(b.Take.AssetID, b.Take.AIVoice, it.takes[b.Take.TakeID].Provider, b.Take.Processed, b.Take.UseProcessed)
			}
		}
		if c := v.Continuous; c != nil {
			narration(c.AssetID, c.AIVoice, "", c.Processed, c.UseProcessed)
		}
	}
	if mx := it.rev.Spec.Mix; mx != nil {
		for _, e := range mx.Enhanced {
			if as, ok := it.assets[e.Variant]; ok {
				m.Parts = append(m.Parts, c2pa.Part{What: "sound", SHA256: as.SHA256, Format: as.MIME, AI: c2pa.AIEnhanced})
			}
		}
	}
	if sc != nil {
		for _, s := range sc.Sources {
			what := "sound"
			switch s.Kind {
			case "video":
				what = "footage"
			case "image":
				what = "picture"
			}
			if slices.ContainsFunc(s.UsedAs, func(u string) bool { return strings.HasSuffix(u, "("+editspec.RoleMusic+")") }) {
				what = "music"
			}
			m.Parts = append(m.Parts, c2pa.Part{What: what, SHA256: s.SHA256, Format: format(s.AssetID)})
		}
	}
	// What the cuts it nests play.
	if it.extras != nil {
		for _, u := range it.extras.NestedCuts(&it.rev.Spec) {
			for _, id := range editspec.AssetIDs(u.Source.Spec) {
				if as, ok := it.extras.Asset(id); ok && as != nil {
					what := map[string]string{"video": "footage", "image": "picture"}[as.Kind]
					if what == "" {
						what = "sound"
					}
					m.Parts = append(m.Parts, c2pa.Part{What: what, SHA256: as.SHA256, Format: as.MIME})
				}
			}
		}
	}
	if sc != nil {
		for _, r := range sc.Resources {
			switch r.Kind {
			case "image", "svg":
				m.Parts = append(m.Parts, c2pa.Part{What: "picture", SHA256: r.SHA256})
			case "pdf":
				m.Parts = append(m.Parts, c2pa.Part{What: "document", SHA256: r.SHA256})
			}
		}
	}
	return m
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
