package aiwork

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// input says how one file sent with a request is made when it is sent: a
// part of a clip (as sound, or as a clip for a dub) or a resources picture.
type input struct {
	role    string
	assetID string
	title   string
	from    float64
	to      float64
	video   bool   // keep the picture too (not used for dubs: their sound is sent)
	rate    int    // sound: samples per second
	wav     bool   // sound as 16-bit WAV (voice copies: what every service takes) instead of FLAC
	format  string // sound: "m4a" for AAC (presenters' sound) instead of FLAC
	clip    bool   // the picture and sound as a video (a presenter's training take or consent reading)
	picture string
	asIs    bool // a picture's own file, not a JPEG (D279)
	footage bool // a part of a clip as an MP4, picture and sound (lip sync, D279)
}

// plan is a request worked out before anything is sent: the model and why,
// the exact settings, what is sent, the price with its alternatives, and
// whether it may go ahead (consent, budget, the key, Content Settings).
type plan struct {
	kind       aiservices.KindInfo
	projectID  string
	choice     *choice
	model      *aiservices.Model
	provider   *aiservices.ProviderInfo
	adapter    aiservices.Adapter
	req        aiservices.Request
	inputs     []input
	est        *aiservices.Estimate
	alts       []aiservices.Alternative
	consentIDs []string
	people     []string
	personID   string // voice copies: whose voice
	use        string // the consent use a voice copy's person must have named (D309)
	feature    string // the origin a feature asked from Go (its keeper takes the result)
	genID      string // the generation's ID, chosen by the feature that asked
	likeness   bool
	sent       []sentPerson // named people whose face or voice it sends (D276)
	noPeople   bool
	sources    []string
	// touched are the projects whose Content Settings the work must follow
	// (D225): the one it is for, every project holding a clip it sends
	// ("" for footage in no project: the default's) and the owner of every
	// picture it sends.
	touched  []string
	consent  []string // why consent is missing (plain)
	problems []string // other reasons it can't go ahead (plain)
	notes    []string
	words    []string // what is sent, in plain words
	now      time.Time
}

var nowFn = func() time.Time { return time.Now().UTC() }

func (p *plan) addConsent(c *models.Consent) {
	for _, id := range p.consentIDs {
		if id == c.ID {
			return
		}
	}
	p.consentIDs = append(p.consentIDs, c.ID)
	p.people = append(p.people, c.PersonName)
	p.likeness = true
	p.sends(sentPerson{id: c.PersonID, name: c.PersonName})
}

// needConsent checks a person's consent for this project, for a use (D274:
// shots, voice, presenters).
func (p *plan) needConsent(ctx context.Context, a *app.App, personID, name, use, why string) bool {
	c, err := generate.ActiveConsentFor(ctx, a, personID, p.projectID, use, p.now)
	if err != nil {
		what := map[string]string{models.ConsentShots: "", models.ConsentVoice: " for their voice", models.ConsentPresenters: " for a presenter"}[use]
		p.consent = append(p.consent, fmt.Sprintf("%s, and MetaRush has no consent from %s%s for this project. Record it on the People page first.", why, name, what))
		return false
	}
	p.addConsent(c)
	return true
}

// planMake works out a request for any kind but video and voice copies.
func planMake(ctx context.Context, a *app.App, in *MakeIn) (*plan, error) {
	k, err := aiservices.KindOf(strings.TrimSpace(in.Kind))
	if err != nil {
		return nil, err
	}
	switch k.Kind {
	case aiservices.KindVideo:
		return nil, apperr.New(apperr.InvalidInput, "Generated video has its own steps: make a shot with Generate video.").WithCommand("metarush generate shot")
	case aiservices.KindVoiceCopy:
		return nil, apperr.New(apperr.InvalidInput, "A copy of someone's voice needs their consent and a person's go-ahead: make it with Copy a voice.").WithCommand("metarush ai voices copy")
	case aiservices.KindPresenterMake:
		return nil, apperr.New(apperr.InvalidInput, "A person's presenter needs their recorded consent and a person's go-ahead: make it with Make a presenter.").WithCommand("metarush presenter start")
	case aiservices.KindPresenter:
		return planPresenter(ctx, a, in, false)
	}
	p := &plan{kind: k, projectID: in.ProjectID, noPeople: in.NoPeople, use: in.Use, feature: in.Feature, genID: in.ID, now: nowFn()}
	if err := p.base(ctx, a, in.Model); err != nil {
		return nil, err
	}
	p.req.Prompt = strings.TrimSpace(in.Prompt)
	p.req.Options = in.Options
	p.req.Options.Name, p.req.Options.ConsentLocale = "", ""
	switch k.Kind {
	case aiservices.KindPicture:
		if len(in.Pictures) > 0 {
			p.req.Task = aiservices.TaskEdit
		}
		view, err := projectlinks.Load(ctx, a, p.projectID)
		if err != nil {
			return nil, err
		}
		for _, ref := range in.Pictures {
			name := ref
			if i := strings.LastIndex(ref, "@"); i > 0 {
				name = ref[:i]
			}
			e, err := resources.Find(ctx, a, p.projectID, view, name)
			if err != nil {
				return nil, err
			}
			if e.Resource.Kind != models.ResourceImage {
				return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a photo or picture file, so it can't be sent with the request.", e.Resource.Title)
			}
			p.inputs = append(p.inputs, input{role: aiservices.RolePicture, picture: ref, title: e.Resource.Title})
			p.touched = append(p.touched, e.Resource.ProjectID)
			p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RolePicture, MIME: "image/jpeg"})
			p.words = append(p.words, "the picture “"+e.Resource.Title+"”")
		}
		p.namesInPrompt(ctx, a)
		if len(p.inputs) > 0 && !p.noPeople && !p.likeness {
			p.consent = append(p.consent, "MetaRush can't tell whether the pictures show real people. Say that they show no one, or record the consent of the people they show.")
		}
	case aiservices.KindBackground, aiservices.KindUpscale:
		if err := p.onePicture(ctx, a, in.Pictures); err != nil {
			return nil, err
		}
	case aiservices.KindLipSync:
		if err := p.lipSync(ctx, a, in); err != nil {
			return nil, err
		}
	case aiservices.KindVoice:
		p.voiceCopyInUse(ctx, a, p.req.Options.Voice)
	case aiservices.KindSoundEffect, aiservices.KindMusic:
		p.namesInPrompt(ctx, a)
	case aiservices.KindVoiceChange, aiservices.KindDubbing, aiservices.KindTranscription:
		role := aiservices.RoleAudio
		rate := 44100
		if k.Kind == aiservices.KindDubbing {
			role = aiservices.RoleMedia
		}
		if k.Kind == aiservices.KindTranscription {
			rate = 16000
			p.req.Options.WordTimes = true
		}
		if k.Kind == aiservices.KindTranscription && (in.From != nil || in.To != nil) {
			return nil, apperr.New(apperr.InvalidInput, "A transcript in the cloud replaces the words of the whole clip, so it can't be of a part.")
		}
		as, from, to, err := clipPart(ctx, a, in.Clip, in.From, in.To)
		if err != nil {
			return nil, err
		}
		p.inputs = append(p.inputs, input{role: role, assetID: as.ID, title: as.DisplayTitle(), from: from, to: to, rate: rate, video: as.Media.HasVideo})
		p.touch(as)
		p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: role, MIME: "audio/flac", Seconds: to - from})
		p.sources = append(p.sources, as.ID)
		p.words = append(p.words, partWords(as, from, to))
		if p.projectID == "" && len(as.ProjectIDs) > 0 && k.Kind != aiservices.KindTranscription {
			p.projectID = as.ProjectIDs[0]
		}
		switch k.Kind {
		case aiservices.KindVoiceChange:
			p.voiceCopyInUse(ctx, a, p.req.Options.Voice)
		case aiservices.KindDubbing:
			if strings.TrimSpace(p.req.Options.TargetLanguage) == "" {
				return nil, apperr.New(apperr.InvalidInput, "Say which language to dub it into (es, fr, de…).")
			}
			if p.req.Options.OwnVoices {
				p.speakersConsent(ctx, a, as, from, to)
			} else {
				p.notes = append(p.notes, "The dub uses similar voices from the service's library, not copies of the speakers' own.")
			}
		}
	}
	if err := generate.RulesAllow(ctx, a, p.model.Provider, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := p.mayLeave(ctx, a); err != nil {
		return nil, err
	}
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	return p, nil
}

// onePicture takes the one picture a picture kind changes (D279): its own
// file is sent, unchanged. A model on this computer (or an ability whose
// network is off) sends it nowhere; a service elsewhere is sent a picture
// that may show people, so it needs "no people" or their consent, as
// pictures sent with any request do (D227).
func (p *plan) onePicture(ctx context.Context, a *app.App, pictures []string) error {
	if len(pictures) != 1 {
		return apperr.New(apperr.InvalidInput, "Choose one picture from the resources library.")
	}
	ref := pictures[0]
	name := ref
	if i := strings.LastIndex(ref, "@"); i > 0 {
		name = ref[:i]
	}
	view, err := projectlinks.Load(ctx, a, p.projectID)
	if err != nil {
		return err
	}
	e, err := resources.Find(ctx, a, p.projectID, view, name)
	if err != nil {
		return err
	}
	r := e.Resource
	if r.Kind != models.ResourceImage {
		return apperr.New(apperr.InvalidInput, "“%s” isn't a photo or picture file.", r.Title)
	}
	mime, w, h := "image/png", 0, 0
	if len(r.Versions) > 0 {
		v := r.Versions[len(r.Versions)-1]
		mime, w, h = firstNonEmpty(v.MIME, mime), v.Width, v.Height
	}
	if lim := p.model.Limits.MaxPixels; lim > 0 && int64(w)*int64(h) > lim {
		return apperr.New(apperr.InvalidInput, "“%s” is %d × %d; %s takes pictures up to about %s pixels.", r.Title, w, h, p.model.Title, megapixels(lim))
	}
	p.inputs = append(p.inputs, input{role: aiservices.RolePicture, picture: ref, title: r.Title, asIs: true})
	p.touched = append(p.touched, r.ProjectID)
	p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RolePicture, MIME: mime})
	p.words = append(p.words, "the picture “"+r.Title+"”")
	if p.kind.Kind == aiservices.KindUpscale && p.req.Options.Scale == 0 && len(p.model.Limits.Scales) > 0 {
		p.req.Options.Scale = p.model.Limits.Scales[0]
	}
	if !aiservices.NothingLeaves(p.provider.Name) && !p.noPeople && !p.likeness {
		p.consent = append(p.consent, "MetaRush can't tell whether the picture shows real people. Say that it shows no one, or record the consent of the people it shows.")
	}
	return nil
}

func megapixels(n int64) string {
	return fmt.Sprintf("%.1f million", float64(n)/1e6)
}

// lipSync takes the footage whose lips are matched and the new sound (D279).
// It changes the faces in that part of the footage, so the on-screen index
// must show who is there: every face named by a person, each with consent
// that names lip sync and this service (consent recorded for other uses
// doesn't cover it); a face nobody named, a part where no one can be told
// apart, or footage not yet looked at is refused. Everyone heard — in the
// footage or the new sound, brief voices too — is held to the same consent,
// and the voice of AI speech to its voice copy's consent. Children and public
// figures are never covered.
func (p *plan) lipSync(ctx context.Context, a *app.App, in *MakeIn) error {
	as, from, to, err := clipPart(ctx, a, in.Clip, in.From, in.To)
	if err != nil {
		return err
	}
	if !as.Media.HasVideo {
		return apperr.New(apperr.InvalidInput, "“%s” has no picture: lip sync needs footage of someone speaking.", as.DisplayTitle())
	}
	snd, sfrom, sto, err := clipPart(ctx, a, in.Sound, nil, nil)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "Choose the sound to match the lips to.").WithDetails(map[string]any{"sound": in.Sound})
	}
	p.inputs = append(p.inputs, input{role: aiservices.RoleMedia, assetID: as.ID, title: as.DisplayTitle(), from: from, to: to, footage: true},
		input{role: aiservices.RoleAudio, assetID: snd.ID, title: snd.DisplayTitle(), from: sfrom, to: sto, rate: 44100})
	p.touch(as)
	p.touch(snd)
	p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleMedia, MIME: "video/mp4", Seconds: to - from},
		aiservices.Media{Role: aiservices.RoleAudio, MIME: "audio/flac", Seconds: sto - sfrom})
	p.sources = append(p.sources, as.ID, snd.ID)
	p.words = append(p.words, "the footage "+partWords(as, from, to), "the sound "+partWords(snd, sfrom, sto))
	if p.projectID == "" && len(as.ProjectIDs) > 0 {
		p.projectID = as.ProjectIDs[0]
	}
	p.likeness = true
	// The faces it changes: only names a person gave count (D274).
	named, unnamed, resembles, known := generate.FacesIn(ctx, a, as, from, to)
	appNamed := generate.FacesSeen(ctx, a, as, from, to).AppNamed
	switch {
	case len(appNamed) > 0:
		p.consent = append(p.consent, fmt.Sprintf("That part of “%s” shows someone named as %s by %s, not by a person. A person names them on the clip, with their consent to lip sync, or pick another part.",
			as.DisplayTitle(), appNamed[0].Name, appNamed[0].By))
	case !known:
		p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't looked at what's on screen in “%s” yet, so it can't tell whose lips it would change. Wait until it has, then name the people in it.", as.DisplayTitle()))
	case unnamed > 0 && len(resembles) > 0:
		p.consent = append(p.consent, fmt.Sprintf("That part of “%s” shows someone who looks like %s, but no person has said so. Name them on the clip, with their consent to lip sync.", as.DisplayTitle(), strings.Join(resembles, " or ")))
	case unnamed > 0:
		p.consent = append(p.consent, fmt.Sprintf("That part of “%s” shows someone nobody has named. Lip sync changes everyone's face it shows: name them, with their consent to lip sync, or pick another part.", as.DisplayTitle()))
	case len(named) == 0:
		p.consent = append(p.consent, fmt.Sprintf("MetaRush sees no face it can tell apart in that part of “%s”, so it can't check whose lips it would change. Pick a part where the speaker's face is seen and named.", as.DisplayTitle()))
	}
	for _, f := range named {
		p.lipConsent(ctx, a, f.PersonID, f.Name, f.Name+"'s face is in the footage")
	}
	// The voices: everyone heard in the footage, and in the new sound.
	p.peopleHeard(ctx, a, as, from, to, "in the footage")
	if g := snd.Source; g != nil && g.Generated != nil && g.Generated.GenerationID != "" {
		// AI speech: the consent its voice relied on (a voice copy), if any.
		if made, err := loadGen(ctx, a, g.Generated.GenerationID); err == nil {
			for _, id := range made.ConsentIDs {
				if c, err := generate.LoadConsent(ctx, a, id); err == nil {
					p.lipConsent(ctx, a, c.PersonID, c.PersonName, "The new sound speaks in "+c.PersonName+"'s voice")
				}
			}
		}
		return nil
	}
	p.peopleHeard(ctx, a, snd, sfrom, sto, "in the new sound")
	return nil
}

// peopleHeard holds everyone speaking in a stretch — brief voices too — to
// lip sync's consent; a voice nobody named is refused.
func (p *plan) peopleHeard(ctx context.Context, a *app.App, as *models.Asset, from, to float64, where string) {
	vs, err := speakers.Voices(ctx, a, as.ID, false)
	if err != nil || len(vs) == 0 {
		p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't told apart the people speaking %s (“%s”) yet, so it can't check their consent. Wait until it has, then name them.", where, as.DisplayTitle()))
		return
	}
	heard := heardLabels(ctx, a, as.ID, from, to)
	for _, v := range vs {
		if heard != nil && !heard[v.Label] {
			continue
		}
		if !v.NamedByPerson() { // only a name a person gave counts (D274)
			p.consent = append(p.consent, fmt.Sprintf("Someone speaking %s (“%s”) hasn't been named by a person%s. Name everyone heard there, with their consent to lip sync.", where, as.DisplayTitle(), namedByWords(&v)))
			continue
		}
		name := firstNonEmpty(v.Name, generate.PersonName(ctx, a, v.PersonID))
		p.lipConsent(ctx, a, v.PersonID, name, upperFirst(name)+" is heard "+where)
	}
}

// lipConsent needs a person's consent in force for this project that names
// lip sync and this service; a child or a public figure is never covered.
func (p *plan) lipConsent(ctx context.Context, a *app.App, personID, name, why string) {
	var cs []models.Consent
	_ = a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": personID, "revokedAt": bson.M{"$exists": false}}, &cs)
	for i := range cs {
		if cs[i].PublicFigure || !cs[i].Adult {
			p.consent = append(p.consent, fmt.Sprintf("%s: MetaRush never changes the likeness of a child or a public figure.", why))
			return
		}
	}
	for i := range cs {
		if cs[i].Covers(p.projectID, p.now) && cs[i].CoversUseAt(models.ConsentUseLipSync, p.model.Provider) {
			p.addConsent(&cs[i])
			return
		}
	}
	p.consent = append(p.consent, fmt.Sprintf("%s, and MetaRush has no consent from %s to lip sync with %s for this project. Record a consent that names lip sync and %s on the People page.",
		why, name, p.provider.Title, p.provider.Title))
}

// mayLeave asks the What may leave part of every project the work touches
// (D290) whether what it sends may go to the model's provider.
func (p *plan) mayLeave(ctx context.Context, a *app.App) error {
	if aiservices.NothingLeaves(p.model.Provider) {
		return nil // a model on this computer, or an ability with its network off (D278, D281)
	}
	var files [][]string
	for _, in := range p.inputs {
		mime := ""
		if in.footage {
			mime = "video/mp4" // lip sync's part of a clip: faces and voices (D279)
		}
		files = append(files, leave.FileKinds(in.role, mime, in.assetID != ""))
	}
	files = append(files, leave.PresenterKinds(p.kind.Kind, p.req.Options.Presenter != "" && !p.req.Options.Stock))
	return leave.Check(ctx, a, leave.AI(p.model.Provider, p.provider.Title, leave.Kinds(p.req.Prompt != "", files...), append([]string{p.projectID}, p.touched...),
		"AI work: "+p.kind.What))
}

// planCopy works out a voice copy (D227).
func planCopy(ctx context.Context, a *app.App, in *CopyIn) (*plan, error) {
	k, _ := aiservices.KindOf(aiservices.KindVoiceCopy)
	p := &plan{kind: k, projectID: in.ProjectID, now: nowFn()}
	if err := p.base(ctx, a, in.Model); err != nil {
		return nil, err
	}
	person, err := ops.ResolvePerson(ctx, a, in.Person)
	if err != nil {
		return nil, err
	}
	p.personID = person.ID
	p.req.Options = models.GenOptions{Name: strings.TrimSpace(in.Name), ConsentLocale: firstNonEmpty(in.Language, "en-US")}
	p.needConsent(ctx, a, person.ID, person.Name, models.ConsentVoice, "A copy of "+person.Name+"'s voice needs their consent")
	as, from, to, err := clipPart(ctx, a, in.Sample, in.From, in.To)
	if err != nil {
		return nil, err
	}
	if as.Source.IsGenerated() {
		return nil, apperr.New(apperr.ConsentRequired, "A generated clip can't be the sample of someone's voice. Use a recording of the person themselves.")
	}
	p.inputs = append(p.inputs, input{role: aiservices.RoleSample, assetID: as.ID, title: as.DisplayTitle(), from: from, to: to, rate: 24000, wav: true})
	p.touch(as)
	p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleSample, MIME: "audio/wav", Seconds: to - from})
	p.sources = append(p.sources, as.ID)
	p.words = append(p.words, "the voice in "+partWords(as, from, to))
	p.speaksAlone(ctx, a, as, from, to, person.ID, person.Name, "the sample")
	if script := p.provider.ConsentScripts[p.req.Options.ConsentLocale]; len(p.provider.ConsentScripts) > 0 {
		if script == "" {
			return nil, apperr.New(apperr.InvalidInput, "%s has no consent script in %s.", p.provider.Title, p.req.Options.ConsentLocale).WithDetails(map[string]any{"languages": keys(p.provider.ConsentScripts)})
		}
		if in.ConsentClip == "" {
			p.consent = append(p.consent, fmt.Sprintf("%s also wants a recording of %s reading its consent script: “%s” Add it, then choose it as the consent recording.", p.provider.Title, person.Name, script))
		} else {
			cs, cfrom, cto, err := clipPart(ctx, a, in.ConsentClip, in.ConsentFrom, in.ConsentTo)
			if err != nil {
				return nil, err
			}
			if cs.Source.IsGenerated() {
				return nil, apperr.New(apperr.ConsentRequired, "A generated clip can't be someone's consent. Use a recording of the person themselves.")
			}
			p.inputs = append(p.inputs, input{role: aiservices.RoleConsent, assetID: cs.ID, title: cs.DisplayTitle(), from: cfrom, to: cto, rate: 24000, wav: true})
			p.touch(cs)
			p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleConsent, MIME: "audio/wav", Seconds: cto - cfrom})
			p.sources = append(p.sources, cs.ID)
			p.words = append(p.words, "the consent read in "+partWords(cs, cfrom, cto))
			if cs.ID == as.ID && math.Max(from, cfrom) < math.Min(to, cto)-0.05 {
				// The reading must be its own recording: the sample can't stand in for consent.
				p.consent = append(p.consent, fmt.Sprintf("The consent recording must be %s reading %s's consent script on its own, not the voice sample: choose another clip, or a part of this one the sample doesn't cover.",
					person.Name, p.provider.Title))
			}
			p.speaksAlone(ctx, a, cs, cfrom, cto, person.ID, person.Name, "the consent recording")
			p.saysScript(ctx, a, cs, cfrom, cto, person.Name, script)
		}
	}
	if err := generate.RulesAllow(ctx, a, p.model.Provider, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := p.mayLeave(ctx, a); err != nil {
		return nil, err
	}
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	return p, nil
}

// base picks the model and its adapter.
func (p *plan) base(ctx context.Context, a *app.App, asked string) error {
	c, err := choose(ctx, a, p.projectID, p.kind.Kind, asked, p.now)
	if err != nil {
		return err
	}
	p.choice, p.model = c, c.model
	if p.provider, err = aiservices.ProviderOf(c.model.Provider); err != nil {
		return err
	}
	if p.adapter, err = aiservices.Get(c.model.Provider, p.kind.Kind); err != nil {
		p.problems = append(p.problems, apperr.From(err).Message)
	}
	p.req = aiservices.Request{Kind: p.kind.Kind, Task: p.kind.Tasks[0], Model: c.model.ID}
	return nil
}

// finish checks the model can do it, prices it and finds the alternatives.
func (p *plan) finish(ctx context.Context, a *app.App) error {
	if err := p.model.Check(&p.req, p.now); err != nil {
		return err
	}
	var err error
	if p.adapter != nil {
		p.est, err = p.adapter.Estimate(&p.req, p.now)
	} else {
		p.est, err = aiservices.EstimateFor(p.model.Provider, p.model, &p.req, p.now)
	}
	if err != nil {
		return err
	}
	p.alts = aiservices.AlternativesWhere(p.model, &p.req, p.est.USD, p.now, func(m *aiservices.Model) bool {
		return allows(p.choice.rules, m.Provider) && built(m.Provider, p.kind.Kind) && readyHere(a, m.Provider, p.kind.Kind, m.ID) == nil
	})
	if p.model.FreeTier {
		p.notes = append(p.notes, p.provider.Title+" has a free tier for this model; on it, the provider may use what you send to improve its products.")
	}
	return p.trainingGate(ctx, a)
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}'’-]+`)

// namesInPrompt finds people the words name (as on the People page): making
// a person's likeness (a picture of them, their voice in music) takes their
// consent.
func (p *plan) namesInPrompt(ctx context.Context, a *app.App) {
	ps, err := speakers.People(ctx, a, false)
	if err != nil || len(ps) == 0 {
		return
	}
	words := map[string]bool{}
	for _, w := range wordRe.FindAllString(p.req.Prompt, -1) {
		words[w] = true
	}
	for _, pp := range ps {
		first := strings.Fields(pp.Name)
		if len(first) == 0 || len([]rune(first[0])) < 3 || !words[first[0]] {
			continue
		}
		use := models.ConsentShots
		if p.kind.Kind != aiservices.KindPicture {
			use = models.ConsentVoice // music or a sound in their voice
		}
		p.needConsent(ctx, a, pp.ID, pp.Name, use, "The words name "+pp.Name)
	}
}

// voiceCopyInUse checks the consent behind a voice copy MetaRush made, when
// the request speaks with it.
func (p *plan) voiceCopyInUse(ctx context.Context, a *app.App, voice string) {
	voice = strings.TrimSpace(voice)
	if voice == "" {
		return
	}
	var g models.Generation
	err := a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": aiservices.KindVoiceCopy, "outcome.outputs.voiceId": voice}).Decode(&g)
	if err != nil {
		return // a provider's own voice
	}
	name := "the voice “" + firstNonEmpty(voiceName(&g), voice) + "”"
	if g.Withdrawn != nil {
		p.consent = append(p.consent, upperFirst(name)+" was withdrawn, so it can't be used any more.")
		return
	}
	for _, id := range g.ConsentIDs {
		c, err := generate.LoadConsent(ctx, a, id)
		if err != nil {
			continue
		}
		why := upperFirst(name) + " is a copy of " + c.PersonName + "'s voice"
		// Speech in a copy of someone's voice always needs their consent for their voice (D227, D274),
		// and a use of its own (new words, D309) a consent naming it and this service as well.
		if !p.needConsent(ctx, a, c.PersonID, c.PersonName, models.ConsentVoice, why) || p.use == "" {
			continue
		}
		// A use of its own (new words in their voice, D309): a consent naming it and this service.
		uc, err := generate.ConsentForUse(ctx, a, c.PersonID, p.projectID, p.use, p.model.Provider, p.now)
		switch {
		case errors.Is(err, generate.ErrChildOrPublic):
			p.consent = append(p.consent, why+": MetaRush never changes the words of a child or a public figure.")
		case err != nil:
			p.consent = append(p.consent, fmt.Sprintf("%s, and MetaRush has no consent from %s for %s with %s in this project. Record a consent that names %s and %s on the People page.",
				why, c.PersonName, useWords(p.use), p.provider.Title, p.use, p.provider.Name))
		default:
			p.addConsent(uc)
		}
	}
}

// useWords says a consent use in plain words.
func useWords(use string) string {
	switch use {
	case models.ConsentUseNewWords:
		return "new words in their own voice"
	case models.ConsentUseLipSync:
		return "lip sync"
	}
	return use
}

// speakersConsent checks that everyone who speaks in a stretch is named and
// has consented (a dub in their own voices copies them).
func (p *plan) speakersConsent(ctx context.Context, a *app.App, as *models.Asset, from, to float64) {
	vs, err := speakers.Voices(ctx, a, as.ID, false)
	if err != nil || len(vs) == 0 {
		p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't told apart the voices in “%s” yet, so it can't check everyone's consent for a dub in their own voices. Wait until it has, or dub with similar voices.", as.DisplayTitle()))
		return
	}
	heard := heardLabels(ctx, a, as.ID, from, to)
	for _, v := range vs {
		if v.Minor || (heard != nil && !heard[v.Label]) {
			continue
		}
		if !v.NamedByPerson() {
			// Only a name a person gave counts (D274), not one an AI app or Auto gave.
			p.consent = append(p.consent, fmt.Sprintf("Someone in “%s” hasn't been named by a person%s. A dub in their own voices copies everyone's voice: name them on the clip and record their consent, or dub with similar voices.",
				as.DisplayTitle(), namedByWords(&v)))
			continue
		}
		p.needConsent(ctx, a, v.PersonID, firstNonEmpty(v.Name, generate.PersonName(ctx, a, v.PersonID)), models.ConsentVoice, "The dub copies "+firstNonEmpty(v.Name, "a speaker")+"'s voice")
	}
}

// namedByWords says who named a voice when it wasn't a person (" — an AI app
// named it Eve"), for messages ("" when nobody named it).
func namedByWords(v *models.AssetSpeaker) string {
	if v.PersonID == "" || v.Source != models.SpeakerByPerson {
		return ""
	}
	return fmt.Sprintf(" (%s named it %s)", v.Naming.Words(), firstNonEmpty(v.Name, "someone"))
}

// speaksAlone checks that a stretch is the person's voice and no one else's
// (a person named the voice).
func (p *plan) speaksAlone(ctx context.Context, a *app.App, as *models.Asset, from, to float64, personID, name, what string) {
	vs, err := speakers.Voices(ctx, a, as.ID, false)
	if err != nil || len(vs) == 0 {
		p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't told apart the voices in %s (“%s”) yet. Wait until it has, then name %s's voice there.", what, as.DisplayTitle(), name))
		return
	}
	heard := heardLabels(ctx, a, as.ID, from, to)
	found := false
	for _, v := range vs {
		if heard != nil && !heard[v.Label] {
			continue
		}
		switch {
		case v.PersonID == personID && v.NamedByPerson():
			found = true
			p.notes = append(p.notes, fmt.Sprintf("%s's voice in %s was named by %s.", name, what, v.Naming.Words()))
		case v.PersonID == personID && v.Source == models.SpeakerByPerson:
			// Named, but not by a person (D274): an AI app, Auto or Claude can't vouch for whose voice it is.
			p.consent = append(p.consent, fmt.Sprintf("The voice in %s (“%s”) was named as %s by %s, not by a person. A person names it there to confirm it.",
				what, as.DisplayTitle(), name, v.Naming.Words()))
			return
		case v.PersonID == personID:
			p.consent = append(p.consent, fmt.Sprintf("Confirm that the voice in %s is %s's: MetaRush only recognized it.", what, name))
			return
		case !v.Minor:
			p.consent = append(p.consent, fmt.Sprintf("Someone other than %s speaks in %s. Pick a part where only %s speaks.", name, what, name))
			return
		}
	}
	if !found {
		p.consent = append(p.consent, fmt.Sprintf("Name %s's voice in %s (“%s”) first, so MetaRush knows it's theirs.", name, what, as.DisplayTitle()))
	}
}

// heardLabels are the voices heard in a stretch (nil when there is no transcript to say).
func heardLabels(ctx context.Context, a *app.App, assetID string, from, to float64) map[string]bool {
	segs, err := transcripts.LoadSegments(ctx, a, assetID)
	if err != nil || len(segs) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, s := range segs {
		if s.To > from && s.From < to && s.Speaker != "" {
			out[s.Speaker] = true
		}
	}
	return out
}

// ReadingPace is how many words a second a script is read at (about 150 a minute).
const ReadingPace = 2.5

// ScriptMatch is how much of a consent script must be heard, in order (D227).
const ScriptMatch = 0.8

// timedWord is a word heard and when.
type timedWord struct {
	text string
	at   float64
}

// saysScript checks the consent recording is the person reading the
// provider's script (D227): at least ScriptMatch of its words, in order
// (the longest common subsequence), within one stretch about as long as
// reading it takes — never words scattered over a long recording.
func (p *plan) saysScript(ctx context.Context, a *app.App, as *models.Asset, from, to float64, who, script string) {
	segs, err := transcripts.LoadSegments(ctx, a, as.ID)
	if err != nil || len(segs) == 0 {
		p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't written down what's said in the consent recording (“%s”) yet, so it can't check the script was read. Try again once its words are there.", as.DisplayTitle()))
		return
	}
	var heard []timedWord
	for _, s := range segs {
		if len(s.Words) > 0 {
			for _, w := range s.Words {
				if w.To > from && w.From < to {
					heard = append(heard, timedWord{norm(w.Text), w.From})
				}
			}
			continue
		}
		if s.To <= from || s.From >= to {
			continue
		}
		ws := wordRe.FindAllString(s.Text, -1)
		for i, w := range ws { // no word times: spread over the line
			heard = append(heard, timedWord{norm(w), s.From + (s.To-s.From)*float64(i)/float64(max(1, len(ws)))})
		}
	}
	sort.SliceStable(heard, func(i, j int) bool { return heard[i].at < heard[j].at })
	var want []string
	for _, w := range wordRe.FindAllString(script, -1) {
		want = append(want, norm(w))
	}
	if !readsScript(heard, want) {
		p.consent = append(p.consent, fmt.Sprintf("The consent recording doesn't have %s reading %s's consent script, in order. It should be them reading: “%s”", who, p.provider.Title, script))
	}
}

// readsScript reports whether some stretch of the words heard, about as long
// as reading the script takes (twice its reading time, or five seconds more,
// whichever is longer), holds at least ScriptMatch of the script in order.
func readsScript(heard []timedWord, want []string) bool {
	if len(want) == 0 {
		return false
	}
	reading := float64(len(want)) / ReadingPace
	window := math.Max(2*reading, reading+5)
	need := int(math.Ceil(ScriptMatch * float64(len(want))))
	for i := range heard {
		j := i
		for j < len(heard) && heard[j].at < heard[i].at+window {
			j++
		}
		if lcs(want, heard[i:j]) >= need {
			return true
		}
	}
	return false
}

// lcs is the length of the longest common subsequence of the script and the words heard.
func lcs(want []string, heard []timedWord) int {
	prev := make([]int, len(heard)+1)
	cur := make([]int, len(heard)+1)
	for i := 1; i <= len(want); i++ {
		for j := 1; j <= len(heard); j++ {
			switch {
			case want[i-1] == heard[j-1].text:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(heard)]
}

func norm(w string) string {
	return strings.Trim(strings.ToLower(strings.ReplaceAll(w, "’", "'")), "'-")
}

// touch notes the projects holding a clip the work sends ("" when it is in
// none: the default's Content Settings apply).
func (p *plan) touch(as *models.Asset) {
	if len(as.ProjectIDs) == 0 {
		p.touched = append(p.touched, "")
	}
	p.touched = append(p.touched, as.ProjectIDs...)
}

// clipPart loads a clip with sound and the stretch asked for.
func clipPart(ctx context.Context, a *app.App, id string, from, to *float64) (*models.Asset, float64, float64, error) {
	if strings.TrimSpace(id) == "" {
		return nil, 0, 0, apperr.New(apperr.InvalidInput, "Choose the clip to use.")
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, 0, 0, apperr.New(apperr.NotFound, "That clip isn't in this workspace.").WithDetails(map[string]any{"assetId": id})
	}
	if !as.Media.HasAudio {
		return nil, 0, 0, apperr.New(apperr.UnsupportedMedia, "“%s” has no sound.", as.DisplayTitle())
	}
	if as.State != models.AssetAvailable {
		return nil, 0, 0, apperr.New(apperr.Conflict, "“%s” is still being added. Try again once it's ready.", as.DisplayTitle())
	}
	f, t := 0.0, as.Media.Duration
	if from != nil {
		f = *from
	}
	if to != nil {
		t = math.Min(*to, as.Media.Duration)
	}
	if f < 0 || t-f < 0.2 {
		return nil, 0, 0, apperr.New(apperr.InvalidInput, "That part of “%s” is too short.", as.DisplayTitle())
	}
	return &as, f, t, nil
}

// partWords names a stretch of a clip: "“Interview” from 0:12 to 1:24", or the whole clip.
func partWords(as *models.Asset, from, to float64) string {
	if from <= 0.05 && to >= as.Media.Duration-0.05 {
		return "“" + as.DisplayTitle() + "”"
	}
	return fmt.Sprintf("“%s” from %s to %s", as.DisplayTitle(), clock(from), clock(to))
}

func clock(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%02d", m, int(s)-m*60)
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// estimate is the plan's price with every check, for people and for cards.
func (p *plan) estimate(ctx context.Context, a *app.App) AIEstimate {
	m := p.model
	e := AIEstimate{ProjectID: p.projectID, Kind: p.kind.Kind, KindTitle: p.kind.Title, What: p.kind.What, Task: p.req.Task, Provider: m.Provider, ProviderTitle: m.ProviderTitle,
		Model: m.ID, ModelTitle: m.Title, Stage: m.Stage, Choice: p.choice.by, Alternatives: p.alts, USD: p.est.USD, Units: p.est.Units, Rate: p.est.Rate,
		PriceTable: p.est.PriceTable, Licence: m.Licence, Data: p.provider.Data.Summary, Watermark: m.Watermark, RetiredOn: m.RetiredOn,
		Inputs: p.words, KeyReady: keyReady(a, m.Provider), Notes: append(append([]string{}, p.notes...), p.est.Notes...)}
	if e.Alternatives == nil {
		e.Alternatives = []aiservices.Alternative{}
	}
	e.Consent = ConsentView{Needed: p.likeness || len(p.consent) > 0, OK: len(p.consent) == 0, People: p.people, ConsentIDs: p.consentIDs}
	if len(p.consent) > 0 {
		e.Consent.Message = p.consent[0]
	} else if p.likeness {
		e.Consent.Message = "Consent recorded for " + joinAnd(p.people) + "."
	}
	if b, err := generate.BudgetView(ctx, a, p.projectID, p.now); err == nil {
		e.Budget = b
	}
	e.Problems = append(append([]string{}, p.consent...), p.problems...)
	if err := readyHere(a, m.Provider, p.kind.Kind, m.ID); err != nil {
		e.KeyReady = false
		e.Problems = append(e.Problems, apperrMessage(err))
	} else if !e.KeyReady {
		e.Problems = append(e.Problems, fmt.Sprintf("%s isn't set up on this computer yet. Add the key in Settings → Integrations.", m.ProviderTitle))
	}
	e.Here = p.provider.Local()
	e.KeyNeeded = p.provider.Route == aiservices.RouteDirect
	if left := e.Budget.Today.LeftUSD; left != nil && e.USD > *left+0.0005 {
		e.Problems = append(e.Problems, fmt.Sprintf("Today's limit has %s left of %s, and this costs about %s.", costs.Money(*left), costs.Money(*e.Budget.Today.LimitUSD), costs.Money(e.USD)))
	}
	if pb := e.Budget.Project; pb != nil && pb.LeftUSD != nil && e.USD > *pb.LeftUSD+0.0005 {
		e.Problems = append(e.Problems, fmt.Sprintf("The project's budget has %s left of %s, and this costs about %s.", costs.Money(*pb.LeftUSD), costs.Money(*pb.LimitUSD), costs.Money(e.USD)))
	}
	e.Allowed = len(e.Problems) == 0
	if m.Licence.Commercial == "plan" || m.Licence.Broadcast == "plan" || m.Licence.Commercial == "unstated" {
		e.Notes = append(e.Notes, "Licence: "+m.Licence.Summary)
	}
	if m.Watermark != "" {
		e.Notes = append(e.Notes, fmt.Sprintf("%s marks what it makes with an invisible %s watermark.", m.ProviderTitle, m.Watermark))
	}
	source := ""
	switch {
	case len(p.words) > 0:
		source = strings.Join(p.words, "; ")
		if p.req.Prompt != "" {
			source = "“" + generate.Clip(p.req.Prompt, 60) + "”; " + source
		}
	case p.req.Prompt != "":
		source = "“" + generate.Clip(p.req.Prompt, 80) + "”"
	}
	parts := p.est.Parts(source)
	for i := range parts {
		parts[i].Limits = generate.AIServicesLimits // paid from generated video's budget and daily limit (D226, D241)
	}
	cost := costs.Of(parts...).Total(p.est.USD)
	cost.Note(fmt.Sprintf("%s's prices of %s.", m.ProviderTitle, costs.Day(p.est.PriceTable)))
	for _, n := range p.est.Notes {
		cost.Note(n)
	}
	e.Cost = cost
	e.Summary = fmt.Sprintf("%s with %s (%s) costs about %s.", upperFirst(p.kind.What), m.ProviderTitle, m.Title, cost.About)
	if cost.About == "" {
		e.Summary = fmt.Sprintf("%s with %s (%s) costs about %s.", upperFirst(p.kind.What), m.ProviderTitle, m.Title, costs.Money(e.USD))
	}
	if e.Here {
		e.Summary = fmt.Sprintf("%s with %s, on this computer: free, and nothing leaves it.", upperFirst(p.kind.What), m.Title)
	}
	if !e.Allowed {
		e.Summary += " " + e.Problems[0]
	}
	return e
}
