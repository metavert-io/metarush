// Package spoken changes a word or short phrase someone said into other
// words in their own voice copy, with their consent (D308–D312): the cut
// plays the new words over the stretch (mix.said), checked by the recognizer
// on this computer and by the voice's fingerprint, and disclosed everywhere.
// The recording and its transcript are never changed.
package spoken

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/voice"
)

// Limits on what is changed (D308): a word or a short phrase.
const (
	MaxWords      = 12  // words changed at once
	MaxNewWords   = 20  // words said in their place
	MaxSeconds    = 6.0 // how long the words changed may be
	contextWords  = 10  // the speaker's words sent on each side, so the new words sound like part of the sentence
	otherVoiceGap = 0.3 // seconds kept clear of anyone else speaking
	// MatchNeeded is how much the voice where the words are must sound like the
	// person's other recordings when they have some (D309: below it, the name
	// may be wrong).
	MatchNeeded = 0.40
)

// SayIn picks the words and says what to say instead.
type SayIn struct {
	ProjectID string `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project."`
	// Branch is the cut (default: the cut whose latest version plays the
	// words; the most recently saved when several do).
	Branch  string   `json:"branch,omitempty" jsonschema:"maxLength=64,description=The cut (default: the one whose latest version plays the words)."`
	AssetID string   `json:"assetId,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=The clip whose words change (with from and to)."`
	From    *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=Where the words start in the clip (seconds)."`
	To      *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where they end."`
	// Quote is the words as the cut says them (a cut sheet's quote): they
	// must be said in exactly one place.
	Quote string `json:"quote,omitempty" jsonschema:"maxLength=400,description=Or: the words as the cut says them (a cut sheet's quote)\\, said in one place."`
	Words string `json:"words" jsonschema:"minLength=1,maxLength=200,description=What to say instead."`
	// Model is the speech model (default: the best one of the service that holds the voice copy).
	Model string `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model of the speech (default: the best of the service holding the voice copy)."`
	// MaxUSD is the price agreed (make only).
	MaxUSD *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most you agree to pay\\, in US dollars (from the estimate). Required unless your estimates are off."`
}

// SayVoice is the voice copy that speaks.
type SayVoice struct {
	GenerationID  string `json:"generationId"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"providerTitle"`
}

// SayCut is a cut whose latest version plays the words.
type SayCut struct {
	Branch string `json:"branch"`
	Title  string `json:"title"`
	// Number is its latest version's number.
	Number int `json:"number"`
}

// SayEstimate is a change worked out before anything is sent: the words,
// whose voice, the voice copy and consent, the cut, the price and whether it
// may go ahead.
type SayEstimate struct {
	ProjectID string  `json:"projectId"`
	Branch    string  `json:"branch,omitempty"`
	CutTitle  string  `json:"cutTitle,omitempty"`
	AssetID   string  `json:"assetId,omitempty"`
	ClipTitle string  `json:"clipTitle,omitempty"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
	Before    string  `json:"before"`
	Words     string  `json:"words"`
	// Context is what the speech service is sent: the new words with the
	// speaker's words around them.
	Context string                `json:"context,omitempty"`
	Speaker *models.SpokenSpeaker `json:"speaker,omitempty"`
	Voice   *SayVoice             `json:"voice,omitempty"`
	// ConsentID is the consent covering new words with the voice copy's service.
	ConsentID  string `json:"consentId,omitempty"`
	Model      string `json:"model,omitempty"`
	ModelTitle string `json:"modelTitle,omitempty"`
	// Cost is the price, part by part (D208); USD its total.
	Cost costs.Estimate `json:"cost"`
	USD  float64        `json:"usd"`
	// Allowed says it may be made; Problems why not (plain words), Code the
	// first problem's kind (consent, speaker, voice, cut, words, here).
	Allowed  bool     `json:"allowed"`
	Problems []string `json:"problems,omitempty"`
	Code     string   `json:"code,omitempty" jsonschema:"enum=consent,enum=speaker,enum=voice,enum=cut,enum=words,enum=here,enum=rules"`
	// Cuts are every cut whose latest version plays the words.
	Cuts    []SayCut `json:"cuts,omitempty"`
	Summary string   `json:"summary"`
	// AI is the speech's own estimate (model and why, alternatives, budget).
	AI *aiwork.AIEstimate `json:"ai,omitempty"`
}

// plan is a change worked out: what the estimate says plus what making it needs.
type plan struct {
	est     SayEstimate
	asset   *models.Asset
	project *models.Project
	segs    []models.TranscriptSegment
	units   []transcripts.Unit
	i, j    int // the words that change
	prevEnd float64
	nextBeg float64
	head    *models.Revision
	copyGen *models.Generation
	make    aiwork.MakeIn
	kinds   []string // each problem's kind
}

func (p *plan) refuse(code, f string, args ...any) {
	if p.est.Code == "" {
		p.est.Code = code
	}
	p.est.Problems = append(p.est.Problems, fmt.Sprintf(f, args...))
	p.kinds = append(p.kinds, code)
}

func (p *plan) codes() []string { return p.kinds }

// CanCheck says whether this computer can check new words (the recognizer
// and voice fingerprints); nil when it can. Tests replace it.
var CanCheck = func(a *app.App) error {
	if err := canHear(a); err != nil {
		return err
	}
	return canFingerprint(a)
}

// planSay works out a change. Hard mistakes in the request (no such clip,
// no words there) are errors; reasons it may not be made are problems.
func planSay(ctx context.Context, a *app.App, in *SayIn) (*plan, error) {
	words := strings.Join(strings.Fields(in.Words), " ")
	if words == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what the new words are.")
	}
	if n := len(strings.Fields(words)); n > MaxNewWords {
		return nil, apperr.New(apperr.InvalidInput, "That's %d words; MetaRush changes a word or a short phrase (at most %d words).", n, MaxNewWords)
	}
	pr, err := ops.ResolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	p := &plan{project: pr}
	p.est.ProjectID, p.est.Words = pr.ID, words
	if err := p.findWords(ctx, a, in); err != nil {
		return nil, err
	}
	p.est.Before = joinUnits(p.units[p.i : p.j+1])
	if transcripts.Norm(p.est.Before) == transcripts.Norm(words) {
		return nil, apperr.New(apperr.InvalidInput, "That's what is said there already (“%s”).", p.est.Before)
	}
	if as := p.asset; as.Source.IsGenerated() {
		p.refuse("words", "“%s” was made with AI; change its words where it was made.", as.DisplayTitle())
	}
	if n := p.j - p.i + 1; n > MaxWords || p.units[p.j].To-p.units[p.i].From > MaxSeconds {
		p.refuse("words", "That's %d words (%.1f seconds); MetaRush changes a word or a short phrase at a time (at most %d words, %.0f seconds).",
			n, p.units[p.j].To-p.units[p.i].From, MaxWords, MaxSeconds)
	}
	p.findCut(ctx, a, in.Branch)
	p.whoSays(ctx, a)
	if p.est.Speaker != nil {
		p.voiceCopy(ctx, a, in.Model)
	}
	if err := CanCheck(a); err != nil {
		p.refuse("here", "This computer can't check new words: %s", lowerFirst(err.Error()))
	}
	p.context()
	if p.copyGen != nil {
		lang := ""
		if l := p.asset.Language; l != nil {
			lang = l.Code
		}
		p.make = aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: pr.ID, Model: p.est.Model, Prompt: p.est.Context,
			Options: models.GenOptions{Voice: voiceIDOf(p.copyGen), Language: lang}, Use: models.ConsentUseNewWords}
		ae, err := aiwork.EstimateFor(ctx, a, &p.make)
		switch {
		case err != nil:
			p.refuse("rules", "%s", plainOf(err))
		default:
			p.est.AI = ae
			p.est.Cost, p.est.USD = ae.Cost, ae.Cost.USD
			p.est.ModelTitle = ae.ModelTitle
			had := contains(p.codes(), "consent")
			for _, s := range ae.Problems {
				consent := strings.Contains(strings.ToLower(s), "consent") || strings.Contains(s, "child or a public figure")
				if consent && had {
					continue // said already, in this feature's words
				}
				code := "rules"
				if consent {
					code = "consent"
				}
				p.refuse(code, "%s", s)
			}
		}
	}
	p.est.Allowed = len(p.est.Problems) == 0
	p.est.Summary = p.summary()
	return p, nil
}

// findWords finds the clip and its words: a clip and a stretch, or a quote
// the cut says once.
func (p *plan) findWords(ctx context.Context, a *app.App, in *SayIn) error {
	assetID, from, to := in.AssetID, 0.0, 0.0
	switch {
	case strings.TrimSpace(in.Quote) != "":
		head, branch, err := headOf(ctx, a, p.project, in.Branch)
		if err != nil {
			return err
		}
		facts, err := ops.LoadFacts(ctx, a, p.project.ID, &head.Spec)
		if err != nil {
			return err
		}
		rs, hint := cutsheet.FindWords(&head.Spec, facts.Transcript, in.Quote, "")
		switch {
		case len(rs) == 0:
			return apperr.New(apperr.NotFound, "The cut doesn't say “%s”.%s", strings.TrimSpace(in.Quote), hintWords(hint))
		case len(rs) > 1:
			return apperr.New(apperr.Conflict, "The cut says “%s” in %d places; pick the clip and the time instead.", strings.TrimSpace(in.Quote), len(rs))
		}
		r := rs[0]
		ci := clipIndex(&head.Spec, r.ClipID)
		if ci < 0 {
			return apperr.New(apperr.NotFound, "The cut doesn't say “%s”.", strings.TrimSpace(in.Quote))
		}
		assetID, from, to = head.Spec.Clips[ci].AssetID, r.WordsFrom, r.WordsTo
		p.head, p.est.Branch = head, branch
	case in.AssetID != "" && in.From != nil && in.To != nil:
		from, to = *in.From, *in.To
	default:
		return apperr.New(apperr.InvalidInput, "Pick the words: a clip with where they start and end, or the words as the cut says them.")
	}
	if to < from {
		from, to = to, from
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return apperr.New(apperr.NotFound, "That clip isn't in this workspace.")
	}
	if editspec.IsConversation(assetID) || editspec.IsNested(assetID) {
		return apperr.New(apperr.InvalidInput, "Words can be changed in a clip's own recording only.")
	}
	segs, err := transcripts.LoadSegments(ctx, a, assetID)
	if err != nil {
		return err
	}
	us := transcripts.Units(segs)
	i, j, ok := transcripts.Range(us, from, to)
	if !ok {
		return apperr.New(apperr.NotFound, "Nothing is said there in “%s”.", as.DisplayTitle())
	}
	for k := i; k <= j; k++ {
		if us[k].Word < 0 {
			return apperr.New(apperr.InvalidInput, "“%s” has no word timings there, so MetaRush can't tell exactly where the words are.", as.DisplayTitle()).
				WithHint("Transcribe it again on this computer, then try once more.")
		}
	}
	p.asset, p.segs, p.units, p.i, p.j = &as, segs, us, i, j
	p.est.AssetID, p.est.ClipTitle = as.ID, as.DisplayTitle()
	p.est.From, p.est.To = us[i].From, us[j].To
	p.prevEnd, p.nextBeg = math.Max(0, us[i].From-0.5), us[j].To+0.5
	if i > 0 {
		p.prevEnd = us[i-1].To
	}
	if j+1 < len(us) {
		p.nextBeg = us[j+1].From
	}
	if d := as.Media.Duration; d > 0 {
		p.nextBeg = math.Min(p.nextBeg, d)
	}
	return nil
}

// findCut finds the cut whose latest version plays the words: the one asked
// for, else the most recently saved (every such cut is listed).
func (p *plan) findCut(ctx context.Context, a *app.App, branch string) {
	uses, err := ops.HeadsUsing(ctx, a, p.asset.ID)
	if err != nil {
		p.refuse("cut", "MetaRush couldn't read the cuts: %s", err.Error())
		return
	}
	var cands []ops.HeadUse
	for _, u := range uses {
		if u.Project.ID == p.project.ID && plays(&u.Revision.Spec, p.asset.ID, p.est.From, p.est.To) {
			cands = append(cands, u)
		}
	}
	sort.SliceStable(cands, func(x, y int) bool { return cands[x].Revision.CreatedAt.After(cands[y].Revision.CreatedAt) })
	for _, u := range cands {
		p.est.Cuts = append(p.est.Cuts, SayCut{Branch: u.Branch, Title: cutTitle(&u.Project, u.Branch), Number: u.Revision.Number})
	}
	want := branch
	if want == "" {
		want = p.est.Branch
	}
	for i := range cands {
		if want == "" || cands[i].Branch == want {
			r := cands[i].Revision
			p.head, p.est.Branch, p.est.CutTitle = &r, cands[i].Branch, cutTitle(p.project, cands[i].Branch)
			return
		}
	}
	if want != "" {
		p.est.Branch = want
		p.refuse("cut", "The latest version of %s doesn't play “%s”; put that part of “%s” in the cut first.", cutTitle(p.project, want), p.est.Before, p.asset.DisplayTitle())
		return
	}
	p.refuse("cut", "No cut of %s plays “%s” yet; put that part of “%s” in a cut first.", p.project.Name, p.est.Before, p.asset.DisplayTitle())
}

// plays reports whether a cut plays [from, to) of a recording whole, at its normal speed.
func plays(s *editspec.Spec, assetID string, from, to float64) bool {
	for _, c := range s.Clips {
		speed := c.Speed
		if speed == 0 {
			speed = 1
		}
		if c.AssetID == assetID && c.In <= from+0.001 && to <= c.Out+0.001 && math.Abs(speed-1) < 1e-6 && !c.Muted {
			return true
		}
	}
	return false
}

// whoSays finds who says the words and how MetaRush knows (D309): one voice,
// named by a person, nobody else speaking close by, and — when the person has
// other recordings — a voice that sounds like them.
func (p *plan) whoSays(ctx context.Context, a *app.App) {
	label := ""
	for k := p.i; k <= p.j; k++ {
		l := p.segs[p.units[k].Seg].Speaker
		switch {
		case l == "":
			p.refuse("speaker", "MetaRush doesn't know who says “%s”: find who's speaking in “%s” first.", p.est.Before, p.asset.DisplayTitle())
			return
		case label != "" && l != label:
			p.refuse("speaker", "Two people say “%s”; change one person's words at a time.", p.est.Before)
			return
		}
		label = l
	}
	lo, hi := p.est.From-otherVoiceGap, p.est.To+otherVoiceGap
	for _, sg := range p.segs {
		if sg.Speaker != label && sg.Speaker != "" && sg.From < hi && lo < sg.To {
			p.refuse("speaker", "Someone else speaks right by “%s”, so the new words would cut into them.", p.est.Before)
			return
		}
	}
	vs, err := speakers.Voices(ctx, a, p.asset.ID, true)
	if err != nil {
		p.refuse("speaker", "MetaRush couldn't read the voices of “%s”.", p.asset.DisplayTitle())
		return
	}
	v := voiceByLabel(vs, label)
	switch {
	case v == nil || v.PersonID == "":
		p.refuse("speaker", "Nobody has named the voice that says “%s” yet: name it first (who's speaking, in the clip's Speakers tab).", p.est.Before)
		return
	case v.Source != models.SpeakerByPerson:
		p.refuse("speaker", "MetaRush only recognized this voice as %s; a person has to confirm it's %s before their words are changed.", v.Name, v.Name)
		return
	case !v.NamedByPerson():
		// Only a name a person gave counts (D274): an AI app, Auto or Claude can't vouch for whose voice it is.
		p.refuse("speaker", "The voice that says “%s” was named %s by %s, not by a person; a person names it to confirm it before their words are changed.", p.est.Before, v.Name, v.Naming.Words())
		return
	}
	sp := &models.SpokenSpeaker{PersonID: v.PersonID, Name: v.Name, Label: v.Label}
	if len(v.Centroid) > 0 {
		if profs, err := speakers.LoadProfiles(ctx, a); err == nil {
			for _, pf := range profs {
				if pf.ID != v.PersonID {
					continue
				}
				m := voice.Score(voice.Cluster{Centroid: v.Centroid}, pf).Score
				sp.Match = &m
				if matchTooLow(m) {
					p.refuse("speaker", "The voice that says “%s” doesn't sound much like %s's other recordings (%.0f%% alike); check who's speaking there first.", p.est.Before, v.Name, m*100)
				}
			}
		}
	}
	p.est.Speaker = sp
}

// matchTooLow: the voice where the words are sounds less like the person's
// other recordings than MatchNeeded (exactly MatchNeeded passes).
func matchTooLow(m float64) bool { return m < MatchNeeded }

func voiceByLabel(vs []models.AssetSpeaker, label string) *models.AssetSpeaker {
	byLabel := map[string]*models.AssetSpeaker{}
	for i := range vs {
		byLabel[vs[i].Label] = &vs[i]
	}
	v := byLabel[label]
	for n := 0; v != nil && v.MergedInto != "" && n < 8; n++ {
		v = byLabel[v.MergedInto]
	}
	return v
}

// voiceCopy finds the person's own voice copy and the consent that covers
// new words with its service (D309); children and public figures never.
func (p *plan) voiceCopy(ctx context.Context, a *app.App, asked string) {
	sp := p.est.Speaker
	var cs []models.Consent
	_ = a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": sp.PersonID}, &cs)
	var ids []string
	for _, c := range cs {
		if c.PublicFigure || !c.Adult {
			p.refuse("consent", "MetaRush never changes the words of a child or a public figure, and %s is recorded as one.", sp.Name)
			return
		}
		ids = append(ids, c.ID)
	}
	var gens []models.Generation
	if len(ids) > 0 {
		_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"kind": aiservices.KindVoiceCopy, "consentIds": bson.M{"$in": ids},
			"outcome.state": models.GenDone, "withdrawn": bson.M{"$exists": false}, "removed": bson.M{"$exists": false}}, &gens,
			options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	}
	var g *models.Generation
	for i := range gens {
		if voiceIDOf(&gens[i]) != "" {
			g = &gens[i]
			break
		}
	}
	if g == nil {
		p.refuse("voice", "There's no copy of %s's voice yet. Only %s's own voice copy, made with their consent, says new words for them.", sp.Name, sp.Name)
		return
	}
	p.copyGen = g
	p.est.Voice = &SayVoice{GenerationID: g.ID, Name: nameOf(g), Provider: g.Provider, ProviderTitle: g.ProviderTitle}
	now, err := generate.Now(ctx, a) // a consent ends by the server's clock (D360)
	var c *models.Consent
	if err == nil {
		c, err = generate.ConsentForUse(ctx, a, sp.PersonID, p.project.ID, models.ConsentUseNewWords, g.Provider, now)
	}
	if err != nil {
		p.refuse("consent", "%s's consent doesn't cover new words in their voice with %s for %s. Record a consent that names new-words and %s.", sp.Name, g.ProviderTitle, p.project.Name, g.Provider)
	} else {
		p.est.ConsentID = c.ID
	}
	p.est.Model = speechModel(g.Provider, asked)
	if p.est.Model == "" {
		p.refuse("voice", "%s holds %s's voice copy but has no speech model MetaRush can use.", g.ProviderTitle, sp.Name)
	} else if prov, _ := aiservices.SplitRef(p.est.Model); prov != g.Provider {
		p.refuse("voice", "%s's voice copy is kept by %s, so the new words are made there, not with %s.", sp.Name, g.ProviderTitle, prov)
	}
}

// speechModel is the speech model to use with a voice copy kept by provider:
// the one asked for, else that service's best.
func speechModel(provider, asked string) string {
	if asked = strings.TrimSpace(asked); asked != "" {
		if m, err := aiservices.FindModel(aiservices.KindVoice, asked); err == nil {
			return m.Provider + "/" + m.ID
		}
		return asked
	}
	var best *aiservices.Model
	now := time.Now()
	for _, m := range aiservices.ModelsOf(aiservices.KindVoice) {
		m := m
		if m.Provider != provider || m.Retired(now) || m.GatewayOnly() {
			continue
		}
		if best == nil || m.Rank > best.Rank {
			best = &m
		}
	}
	if best == nil {
		return ""
	}
	return best.Provider + "/" + best.ID
}

// context is what the service is asked to say: the new words with up to ten
// of the speaker's words on each side, within the line, so the new words are
// spoken as part of the sentence (D310). Only the new words are kept.
func (p *plan) context() {
	if p.units == nil {
		return
	}
	seg := p.units[p.i].Seg
	var before, after []string
	for k := p.i - 1; k >= 0 && len(before) < contextWords && p.units[k].Seg == seg; k-- {
		before = append([]string{p.units[k].Text}, before...)
	}
	for k := p.j + 1; k < len(p.units) && len(after) < contextWords && p.units[k].Seg == p.units[p.j].Seg; k++ {
		after = append(after, p.units[k].Text)
	}
	parts := append(append(before, p.est.Words), after...)
	p.est.Context = strings.Join(parts, " ")
}

func (p *plan) summary() string {
	who := "the speaker"
	if p.est.Speaker != nil {
		who = p.est.Speaker.Name
	}
	s := fmt.Sprintf("Say “%s” in place of “%s” in %s's own voice copy", p.est.Words, p.est.Before, who)
	if p.est.CutTitle != "" {
		s += " in " + p.est.CutTitle
	}
	if p.est.AI != nil {
		s += fmt.Sprintf(" (about %s)", costs.Money(p.est.USD))
	}
	s += "."
	if !p.est.Allowed && len(p.est.Problems) > 0 {
		s += " " + p.est.Problems[0]
	}
	return s
}

// headOf loads a cut's latest version (the main cut when none is named).
func headOf(ctx context.Context, a *app.App, pr *models.Project, branch string) (*models.Revision, string, error) {
	if branch == "" {
		branch = "main"
	}
	id := pr.Branches[branch].Head
	if id == "" {
		return nil, branch, apperr.New(apperr.NotFound, "%s has no cut called %s yet.", pr.Name, branch)
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		return nil, branch, apperr.New(apperr.NotFound, "The latest version of that cut is missing.")
	}
	return &r, branch, nil
}

func clipIndex(s *editspec.Spec, id string) int {
	for i, c := range s.Clips {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// cutTitle names a cut as people see it.
func cutTitle(pr *models.Project, branch string) string {
	if b, ok := pr.Branches[branch]; ok && strings.TrimSpace(b.Title) != "" {
		return "“" + strings.TrimSpace(b.Title) + "”"
	}
	if branch == "main" || branch == "" {
		return "the cut of " + pr.Name
	}
	return "the cut “" + branch + "” of " + pr.Name
}

func joinUnits(us []transcripts.Unit) string {
	var ws []string
	for _, u := range us {
		ws = append(ws, u.Text)
	}
	return strings.Join(ws, " ")
}

func voiceIDOf(g *models.Generation) string {
	if g == nil || g.Outcome == nil {
		return ""
	}
	for _, o := range g.Outcome.Outputs {
		if o.VoiceID != "" {
			return o.VoiceID
		}
	}
	return ""
}

func nameOf(g *models.Generation) string {
	if g.Options != nil && strings.TrimSpace(g.Options.Name) != "" {
		return strings.TrimSpace(g.Options.Name)
	}
	return "a voice copy"
}

func hintWords(h string) string {
	if strings.TrimSpace(h) == "" {
		return ""
	}
	return " " + strings.TrimSpace(h)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && r[1] >= 'A' && r[1] <= 'Z' {
		return s
	}
	return strings.ToLower(string(r[:1])) + string(r[1:])
}

// plainOf is an error's plain message.
func plainOf(err error) string {
	return apperr.From(err).Message
}
