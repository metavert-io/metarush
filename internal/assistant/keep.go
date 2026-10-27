package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/keep"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// "Keep this?" cards (D166, docs/keeping.md). When a conversation brings in
// something reusable, MetaRush asks once, with a card the person keeps or
// declines ("Not now"): never automatic (a keep card is never a safe step) and
// never twice (keep.Ask: one record per thing, by its content, per project).
// The cards come from two places:
//   - Claude's `metarush keep <kind> …` commands (and the long ways of saying
//     the same, `project rules add`, `cut signoffs add`, `improve add`),
//     refused for Claude like every change and turned into cards here;
//   - MetaRush's own detectors at the end of each turn: links the person typed,
//     files they attached, and the logo a page snapshot found (D165).
//
// Every turn's message also carries what the project keeps (its rules, brief,
// sign-offs still waiting, and what was offered before), so Claude follows the
// rules and doesn't offer the same thing twice.

// KeepCard makes a card a "Keep this?" card.
type KeepCard struct {
	// ItemID is the record that asks (kep_…): kept or declined with the card.
	ItemID string `bson:"itemId" json:"itemId"`
	Kind   string `bson:"kind" json:"kind" jsonschema:"enum=source,enum=picture,enum=brief,enum=rule,enum=promise,enum=person,enum=signoff,enum=check,enum=improve,enum=spelling"`
	// Text is the thing in the person's words: the rule, the sign-off, the
	// fact, the word, the address.
	Text string `bson:"text,omitempty" json:"text,omitempty"`
	// Does says what keeping does, in one plain sentence.
	Does string `bson:"does" json:"does"`
	// From says who noticed it: MetaRush (a link the person typed, a file they
	// attached, a logo a page had) or Claude.
	From string `bson:"from" json:"from" jsonschema:"enum=metarush,enum=claude"`
	// Colors are a logo's brand colours (#RRGGBB), shown as swatches.
	Colors []string `bson:"colors,omitempty" json:"colors,omitempty"`
	// MIME is an attached file's type (a picture or a PDF).
	MIME string `bson:"mime,omitempty" json:"mime,omitempty" jsonschema:"description=An attached file's type (image/png\\, application/pdf…)."`
	// Where asks where a rule or check is kept (D193): just this project, or
	// every project using the Content Settings it uses (a house rule). Keep
	// sends the choice (assistant.act keepWhere); the first is the default.
	Where []KeepWhere `bson:"where,omitempty" json:"where,omitempty"`
}

// KeepWhere is one place a rule can be kept.
type KeepWhere struct {
	Value string `bson:"value" json:"value" jsonschema:"enum=project,enum=settings"`
	Label string `bson:"label" json:"label"`
}

// keepCommands are Claude's ways of offering to keep something, by kind.
var keepCommands = map[string]string{
	"keep source": models.KeepSource, "keep logo": models.KeepPicture, "keep picture": models.KeepPicture, "keep brief": models.KeepBrief,
	"keep rule": models.KeepRule, "keep promise": models.KeepPromise, "keep check": models.KeepCheck, "keep person": models.KeepPerson, "keep signoff": models.KeepSignoff,
	"keep improve": models.KeepImprove, "keep spelling": models.KeepSpelling,
	// The same things said the long way.
	"project rules add": models.KeepRule, "cut signoffs add": models.KeepSignoff, "improve add": models.KeepImprove,
}

// memoryCommands change what a project keeps (cards, but not "Keep this?" ones).
var memoryCommands = map[string]bool{"project rules remove": true, "cut signoffs done": true, "cut signoffs undo": true, "cut signoffs remove": true,
	"keep decline": true, "footage find-switches": true}

// keepTitle is the card's question, by kind.
func keepTitle(kind string, in ops.KeepAddIn) string {
	d := in.Detail
	switch kind {
	case models.KeepSource:
		return "Add " + linkShown(orStr(d.URL, in.Text)) + " to this project's sources?"
	case models.KeepPicture:
		switch {
		case d.BrandKit || d.FromSite != "":
			return "Keep this logo in the sources and the brand kit?"
		case d.File != nil && d.File.MIME == "application/pdf":
			return "Keep this PDF in the project's sources?"
		}
		return "Keep this picture in the project's sources?"
	case models.KeepBrief:
		return "Keep this in the project's brief?"
	case models.KeepRule:
		return "Keep this rule for the project?"
	case models.KeepPromise:
		return "Keep this as a promise to the client?"
	case models.KeepCheck:
		return "Check this in every quality check of the project?"
	case models.KeepPerson:
		return "Add this to " + orStr(d.Person, "their") + "'s notes?"
	case models.KeepSignoff:
		return "Add this sign-off to the cut?"
	case models.KeepImprove:
		return "Add this to MetaRush's improvement list?"
	case models.KeepSpelling:
		return "Put " + quote(in.Text) + " on this project's spelling list?"
	}
	return "Keep this?"
}

// linkShown is an address as a person reads it: its site and page, not the
// scheme ("fernbrook.example.org", "fernbrook.example.org/about").
func linkShown(raw string) string {
	k := keep.LinkKey(raw)
	if len(k) > 60 {
		if i := strings.IndexAny(k, "/?"); i > 0 {
			return k[:i]
		}
		return clip(k, 60)
	}
	return k
}

func orStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// describeKeep turns Claude's `metarush keep …` into a "Keep this?" card, or
// into nothing when the thing was offered before in the project.
func (m *Manager) describeKeep(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	kind := keepCommands[call.Path]
	args := call.Args
	arg := func(i int) string {
		if i < len(args) {
			return strings.TrimSpace(args[i])
		}
		return ""
	}
	project := strings.TrimSpace(call.Flags["project"])
	in := ops.KeepAddIn{Kind: kind, From: "claude"}
	switch call.Path {
	case "project rules add", "cut signoffs add":
		project, in.Text = arg(0), arg(1)
		if call.Path == "project rules add" && hasBool(call, "check") {
			in.Kind = models.KeepCheck
		}
		if call.Path == "project rules add" && hasBool(call, "promise") {
			in.Kind = models.KeepPromise // a promise made to the client (D301)
		}
	case "improve add":
		in.Text = arg(0)
		in.Detail.Why = clip(strings.TrimSpace(call.Flags["description"]), 600)
	case "keep person":
		in.Detail.Person, in.Text = arg(0), arg(1)
	case "keep brief":
	default:
		in.Text = arg(0)
	}
	in.Text = clip(in.Text, 600)
	if w := strings.TrimSpace(call.Flags["why"]); w != "" {
		in.Detail.Why = clip(w, 600)
	}
	title := keepTitle(in.Kind, in)
	if project == "" {
		project = c.projectID()
	}
	var p *models.Project
	if ops.KeepScope(in.Kind, "x") != "" {
		if project == "" {
			incomplete(a, title, "Claude did not say which project to keep it in.")
			return
		}
		var err error
		if p, err = ops.ResolveProject(ctx, m.App, project); err != nil {
			incomplete(a, title, apperr.From(err).Message)
			return
		}
		in.ProjectID = p.ID
	}
	switch in.Kind {
	case models.KeepSource:
		if keep.LinkKey(in.Text) == "" || intake.RouteLink(in.Text).Route == "invalid" {
			incomplete(a, title, "That isn't a web address MetaRush can use.")
			return
		}
		in.Detail.URL = in.Text
	case models.KeepPicture:
		if why := m.pictureDetail(ctx, c, p, call, &in); why != "" {
			incomplete(a, title, why)
			return
		}
		title = keepTitle(in.Kind, in)
	case models.KeepBrief:
		in.Detail.Goal = clip(strings.TrimSpace(call.Flags["goal"]), 600)
		in.Detail.Audience = clip(strings.TrimSpace(call.Flags["audience"]), 600)
		in.Detail.Tone = clip(strings.TrimSpace(call.Flags["tone"]), 300)
		ds := call.Multi["deliverable"]
		if len(ds) == 0 && call.Flags["deliverable"] != "" {
			ds = []string{call.Flags["deliverable"]}
		}
		for _, x := range ds {
			if x = strings.TrimSpace(x); x != "" && len(in.Detail.Deliverables) < 12 {
				in.Detail.Deliverables = append(in.Detail.Deliverables, models.Deliverable{Name: clip(x, 80)})
			}
		}
		if in.Detail.Goal == "" && in.Detail.Audience == "" && in.Detail.Tone == "" && len(in.Detail.Deliverables) == 0 {
			incomplete(a, title, "Claude did not say what to keep in the brief.")
			return
		}
	case models.KeepRule, models.KeepCheck:
		in.Detail.Measure = strings.TrimSpace(call.Flags["measure"])
		if in.Text == "" {
			incomplete(a, title, "Claude did not say the rule.")
			return
		}
		if in.Detail.Measure != "" && !ops.ValidMeasure(in.Detail.Measure) {
			incomplete(a, title, "MetaRush can't measure that.")
			return
		}
	case models.KeepPromise:
		// A promise made to the client (D301): the person keeps it; it must be measurable.
		in.Detail.Measure = strings.TrimSpace(call.Flags["measure"])
		if in.Text == "" {
			incomplete(a, title, "Claude did not say the promise.")
			return
		}
		if in.Detail.Measure != "" && !ops.ValidMeasure(in.Detail.Measure) {
			incomplete(a, title, "MetaRush can't measure that.")
			return
		}
		if in.Detail.Measure == "" && ops.MeasureFor(in.Text) == "" {
			incomplete(a, title, "MetaRush can't measure that promise, so it can't hold a version back for it: keep it as a sign-off instead.")
			return
		}
	case models.KeepPerson:
		if in.Detail.Person == "" || in.Text == "" {
			incomplete(a, title, "Claude did not say who it is about and what to note.")
			return
		}
		person, err := ops.ResolvePerson(ctx, m.App, in.Detail.Person)
		if err != nil {
			incomplete(a, title, apperr.From(err).Message)
			return
		}
		in.Detail.Person = person.Name
		title = keepTitle(in.Kind, in)
	case models.KeepSpelling:
		in.Detail.HeardAs = call.Multi["heard-as"]
		if len(in.Detail.HeardAs) == 0 && call.Flags["heard-as"] != "" {
			in.Detail.HeardAs = []string{call.Flags["heard-as"]}
		}
		fallthrough
	default:
		if in.Text == "" {
			incomplete(a, title, "Claude did not say what to keep.")
			return
		}
	}
	m.offerKeep(ctx, c, a, in, title)
}

// offerKeep asks about a thing once: a card, or nothing when the project was
// asked before (the turn's context then lists it as offered).
func (m *Manager) offerKeep(ctx context.Context, c *conv, a *AssistantAction, in ops.KeepAddIn, title string) bool {
	in.ConversationID = c.id
	it := models.KeepItem{ProjectID: ops.KeepScope(in.Kind, in.ProjectID), Kind: in.Kind, Text: in.Text, Detail: in.Detail, From: in.From,
		ConversationID: c.id, ActionID: a.ID}
	rec, err := keep.Ask(ctx, m.App.DB, m.App.WorkspaceID(), it)
	if errors.Is(err, keep.ErrSeen) {
		incomplete(a, title, seenWords(rec))
		return false
	}
	if err != nil {
		incomplete(a, title, "MetaRush couldn't remember this one.")
		return false
	}
	projectName := ""
	if in.ProjectID != "" {
		projectName = m.nameOf(ctx, in.ProjectID)
	}
	var st ops.KeepSettings
	if in.ProjectID != "" {
		st = ops.KeepSettingsOf(ctx, m.App, in.ProjectID)
	}
	does := ops.KeepDescribe(in.Kind, in.Text, in.Detail, quotedName(projectName), st)
	if in.Kind == models.KeepSpelling && st.Owner != "" && !st.Own {
		title = "Put " + quote(in.Text) + " on the spelling list of " + quote(st.OwnerName) + "'s settings?"
	}
	a.Title = title
	a.Kind = orStr(a.Kind, "keep")
	a.State = ActionPending
	a.Keeping = &KeepCard{ItemID: rec.ID, Kind: in.Kind, Text: keepShownText(in), Does: does, From: in.From}
	if (in.Kind == models.KeepRule || in.Kind == models.KeepCheck || in.Kind == models.KeepSignoff || in.Kind == models.KeepPromise) && st.Owner != "" {
		// Where it goes (D193, D200): this project, or every project using these settings.
		every := "Every project using " + quote(st.OwnerName) + "'s settings"
		if st.Own {
			every = "Every project using this project's settings"
		}
		a.Keeping.Where = []KeepWhere{{Value: "project", Label: "Just this project"}, {Value: "settings", Label: every}}
	}
	if in.Detail.File != nil {
		a.Keeping.MIME = in.Detail.File.MIME
	}
	a.Details = nil
	if t := keepShownText(in); t != "" && in.Kind != models.KeepSource && in.Kind != models.KeepSpelling && in.Kind != models.KeepPicture {
		a.Details = append(a.Details, quote(t))
	}
	a.Details = append(a.Details, does)
	setInput(a, "keep.add", in)
	return true
}

// quotedName is a project's name as a card says it (the plain fallback stays bare).
func quotedName(n string) string {
	switch n {
	case "", "the project":
		return n
	}
	return quote(n)
}

// keepShownText is the thing in words for the card.
func keepShownText(in ops.KeepAddIn) string {
	if in.Kind == models.KeepBrief {
		var parts []string
		if in.Detail.Goal != "" {
			parts = append(parts, in.Detail.Goal)
		}
		if in.Detail.Audience != "" {
			parts = append(parts, "for "+in.Detail.Audience)
		}
		if in.Detail.Tone != "" {
			parts = append(parts, in.Detail.Tone)
		}
		var ds []string
		for _, d := range in.Detail.Deliverables {
			ds = append(ds, d.Name)
		}
		if len(ds) > 0 {
			parts = append(parts, strings.Join(ds, ", "))
		}
		return strings.Join(parts, "; ")
	}
	if in.Kind == models.KeepPicture && in.Detail.File != nil {
		return in.Detail.File.Name
	}
	return in.Text
}

// seenWords says why a thing isn't asked again (Claude's refusal reason).
func seenWords(rec *models.KeepItem) string {
	if rec == nil {
		return "It was offered before: not asked again."
	}
	switch rec.State {
	case models.KeepKept:
		return "Already kept: not asked again."
	case models.KeepDeclined:
		return "The person said not now before: not asked again."
	}
	return "Already asked: a card waits for the person."
}

// pictureDetail fills a picture's details from Claude's words: a file the
// person attached (by its name or att_…), a picture in the sources (its name),
// or a picture's address; --brand makes it the brand kit's logo, --from-site
// takes a website snapshot's brand. It returns why it can't, or "".
func (m *Manager) pictureDetail(ctx context.Context, c *conv, p *models.Project, call *cliCall, in *ops.KeepAddIn) string {
	ref := strings.TrimSpace(in.Text)
	in.Detail.BrandKit = hasBool(call, "brand")
	if site := strings.TrimSpace(call.Flags["from-site"]); site != "" {
		e, err := m.findResource(ctx, p, site)
		if err != nil || e.Kind != models.ResourceWebsite {
			return "MetaRush can't find that website snapshot in the project."
		}
		in.Detail.FromSite = e.ID
	}
	if f := c.attachmentNamed(ref); f != nil {
		in.Detail.File, in.Detail.Attachment = f, f.SHA256
		in.Text = f.Name
		if !in.Detail.BrandKit && looksLikeLogo(f.Name, f.MIME, f.Width, f.Height) {
			in.Detail.BrandKit = true
		}
		return ""
	}
	if strings.Contains(ref, "://") {
		if keep.LinkKey(ref) == "" {
			return "That isn't a picture's web address."
		}
		in.Detail.URL = ref
		return ""
	}
	if ref == "" {
		if in.Detail.FromSite == "" {
			return "Claude did not say which picture."
		}
		return ""
	}
	e, err := m.findResource(ctx, p, ref)
	if err != nil {
		return "MetaRush can't find that picture in the project's sources or this conversation."
	}
	if e.Kind != models.ResourceImage && e.Kind != models.ResourceSVG {
		return "That isn't a picture."
	}
	in.Detail.ResourceID = e.ID
	in.Text = e.Name
	return ""
}

// findResource finds a resource the project can use, by its name or ID.
func (m *Manager) findResource(ctx context.Context, p *models.Project, ref string) (*models.Resource, error) {
	pid := ""
	var view *projectlinks.View
	if p != nil {
		pid = p.ID
		view, _ = projectlinks.Load(ctx, m.App, p.ID)
	}
	e, err := resources.Find(ctx, m.App, pid, view, ref)
	if err != nil {
		return nil, err
	}
	return &e.Resource, nil
}

// looksLikeLogo: a picture named as a logo or icon, an SVG, or a small PNG,
// WebP or GIF (a photo — a JPEG, or anything large — isn't one).
func looksLikeLogo(name, mime string, w, h int) bool {
	n := strings.ToLower(name)
	for _, k := range []string{"logo", "icon", "brand", "emblem", "badge"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	switch {
	case mime == "image/svg+xml" || strings.HasSuffix(n, ".svg"):
		return true
	case mime == "image/jpeg" || mime == "application/pdf" || strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".jpeg"):
		return false
	}
	if w > 0 && h > 0 && w <= 800 && h <= 800 {
		r := float64(w) / float64(h)
		return r >= 0.25 && r <= 4
	}
	return false
}

// describeMemory is a card for a change to what a project keeps that isn't a
// "Keep this?" (ticking a sign-off, taking a rule out) or for finding a call
// recording's picture switches.
func (m *Manager) describeMemory(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	args := call.Args
	arg := func(i int) string {
		if i < len(args) {
			return strings.TrimSpace(args[i])
		}
		return ""
	}
	switch call.Path {
	case "keep decline":
		incomplete(a, "Decline something", "Only the person says not now, on the card.")
	case "footage find-switches":
		ref := arg(0)
		if ref == "" {
			incomplete(a, "Find where the picture switches", "Claude did not name the clip.")
			return
		}
		a.Title = "Find where the picture switches in " + m.cardName(ctx, ref)
		a.Details = []string{"Looks at the picture around each switch between people, to the frame, on this computer"}
		setInput(a, "footage.switches.find", ops.FootageSwitchesFindIn{AssetID: ref})
	case "project rules remove":
		project, rule := orStr(arg(0), c.projectID()), arg(1)
		if project == "" || rule == "" {
			incomplete(a, "Take a rule out", "Claude did not name the project and the rule.")
			return
		}
		p, err := ops.ResolveProject(ctx, m.App, project)
		if err != nil {
			incomplete(a, "Take a rule out", apperr.From(err).Message)
			return
		}
		text := ruleText(p, rule)
		if text == "" {
			incomplete(a, "Take a rule out", "The project has no such rule.")
			return
		}
		a.Title = "Take the rule " + quote(text) + " out of the project"
		a.Details = []string{"Claude stops following it and the quality check stops checking it"}
		setInput(a, "project.rules.remove", ops.RuleRemoveIn{ProjectID: p.ID, Rule: rule})
	case "cut signoffs done", "cut signoffs undo", "cut signoffs remove":
		project, ref := orStr(arg(0), c.projectID()), arg(1)
		verb := strings.TrimPrefix(call.Path, "cut signoffs ")
		if project == "" || ref == "" {
			incomplete(a, "Change a sign-off", "Claude did not name the project and the sign-off.")
			return
		}
		p, err := ops.ResolveProject(ctx, m.App, project)
		if err != nil {
			incomplete(a, "Change a sign-off", apperr.From(err).Message)
			return
		}
		text := signoffText(p, call.Flags["branch"], ref)
		if text == "" {
			incomplete(a, "Change a sign-off", "The cut has no such sign-off.")
			return
		}
		switch verb {
		case "done":
			a.Title = "Tick the sign-off " + quote(text)
			a.Details = []string{"Only when that OK was really given"}
		case "undo":
			a.Title = "Untick the sign-off " + quote(text)
			a.Details = []string{"The OK is needed again"}
		default:
			a.Title = "Take the sign-off " + quote(text) + " off the checklist"
		}
		in := ops.SignoffDecideIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID, Branch: call.Flags["branch"]}, Signoff: ref, Note: clip(call.Flags["note"], 300)}
		setInput(a, "cut.signoffs."+verb, in)
	}
}

func ruleText(p *models.Project, ref string) string {
	for i, r := range p.Rules {
		if r.ID == ref || keep.Norm(r.Text) == keep.Norm(ref) || ref == itoa(i+1) {
			return r.Text
		}
	}
	return ""
}

func signoffText(p *models.Project, branch, ref string) string {
	if branch == "" {
		branch = "main"
	}
	n := 0
	for _, s := range p.Signoffs {
		b := s.Branch
		if b == "" {
			b = "main"
		}
		if b != branch {
			continue
		}
		n++
		if s.ID == ref || keep.Norm(s.Text) == keep.Norm(ref) || ref == itoa(n) {
			return s.Text
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// keepLabel describes the keep, rules, sign-off and call-recording commands
// as a step in the thread.
func keepLabel(c *cliCall) string {
	switch c.Path {
	case "keep source":
		return "Asked to keep a link in the sources"
	case "keep logo", "keep picture":
		return "Asked to keep a picture"
	case "keep brief":
		return "Asked to keep the brief"
	case "keep rule":
		return "Asked to keep a rule for the project"
	case "project rules add":
		if hasBool(c, "check") {
			return "Asked to check something every time"
		}
		return "Asked to keep a rule for the project"
	case "keep check":
		return "Asked to check something every time"
	case "keep person":
		return "Asked to add a note about someone"
	case "keep signoff", "cut signoffs add":
		return "Asked to add a sign-off"
	case "keep improve", "improve add":
		return "Asked to add an idea for MetaRush"
	case "keep spelling":
		return "Asked to keep how a word is spelled"
	case "keep list":
		return "Looked at what the project keeps"
	case "keep decline":
		return "Asked to set something aside"
	case "project rules", "project rules list":
		return "Read the project's rules"
	case "project rules remove":
		return "Asked to take a rule out"
	case "cut signoffs", "cut signoffs list":
		return "Read the sign-offs"
	case "cut signoffs done":
		return "Asked to tick a sign-off"
	case "cut signoffs undo":
		return "Asked to untick a sign-off"
	case "cut signoffs remove":
		return "Asked to take a sign-off off"
	case "footage switches":
		return "Looked at where the picture switches"
	case "footage find-switches":
		return "Asked to find where the picture switches"
	case "cut flashes":
		return "Asked to trim the flashes at the clips' edges"
	case "cut names":
		return "Asked to cover the printed names with a name bar"
	}
	return ""
}

/* ---------- Detectors ---------- */

// maxKeepLinks: link cards per message at most.
const maxKeepLinks = 3

// addKeepCards runs MetaRush's detectors at the end of a turn: links the
// person typed in the message and files they attached become "Keep this?"
// cards, unless a card of the conversation already brings them in, the project
// has them, or they were offered before.
func (m *Manager) addKeepCards(c *conv, turn int) {
	c.mu.Lock()
	if turn < 1 || turn > len(c.data.Turns) {
		c.mu.Unlock()
		return
	}
	t := c.data.Turns[turn-1]
	project := t.Context.ProjectID
	if project == "" {
		project = c.data.ProjectID
	}
	covered := map[string]bool{}
	for _, a := range c.data.Actions {
		for _, k := range linkKeysIn(a.Input) {
			covered[k] = true
		}
	}
	c.mu.Unlock()
	if t.Note || project == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var cards []AssistantAction
	text := t.Prompt
	if t.AnswerTo != "" && t.Shown != "" {
		text = t.Shown // a question's answer: the person's words, not the question
	}
	links := personLinks(text)
	have := map[string]bool{}
	if len(links) > 0 {
		have = m.projectLinks(ctx, project)
	}
	n := 0
	for _, l := range links {
		k := keep.LinkKey(l)
		if covered[linkKey(l)] || covered[k] || have[k] || n >= maxKeepLinks {
			continue
		}
		a := AssistantAction{ID: shortID("act"), Turn: turn, Kind: "keep", State: ActionPending, CreatedAt: time.Now().UTC()}
		in := ops.KeepAddIn{ProjectID: project, Kind: models.KeepSource, Text: l, Detail: models.KeepDetail{URL: l, Why: "You typed it in this conversation."}, From: "metarush"}
		if m.offerKeep(ctx, c, &a, in, keepTitle(models.KeepSource, in)) {
			cards = append(cards, a)
			n++
		}
	}
	for _, f := range t.Attachments {
		f := f
		a := AssistantAction{ID: shortID("act"), Turn: turn, Kind: "keep", State: ActionPending, CreatedAt: time.Now().UTC()}
		in := ops.KeepAddIn{ProjectID: project, Kind: models.KeepPicture, Text: f.Name, Detail: models.KeepDetail{Attachment: f.SHA256, File: &f,
			BrandKit: looksLikeLogo(f.Name, f.MIME, f.Width, f.Height), Why: "You attached it in this conversation."}, From: "metarush"}
		if m.offerKeep(ctx, c, &a, in, keepTitle(models.KeepPicture, in)) {
			a.Image = m.attachmentImage(c.id, f)
			cards = append(cards, a)
		}
	}
	m.addCards(c, turn, cards)
}

// addCards adds cards MetaRush made itself to a conversation.
func (m *Manager) addCards(c *conv, turn int, cards []AssistantAction) {
	if len(cards) == 0 {
		return
	}
	c.mu.Lock()
	for _, a := range cards {
		a.Turn = turn
		c.data.Actions = append(c.data.Actions, a)
		c.addEvent(AssistantEvent{Kind: "action", Turn: turn, ActionID: a.ID, Text: a.Title})
	}
	if len(c.data.Actions) > maxActions {
		c.data.Actions = c.data.Actions[len(c.data.Actions)-maxActions:]
	}
	c.touch()
	c.mu.Unlock()
	m.changed(c.id)
}

// commonTLDs are the endings a bare address ("fernbrook.example.org") must have to count
// as a link (a file name such as notes.md or clip.mov doesn't).
var commonTLDs = map[string]bool{"com": true, "org": true, "net": true, "edu": true, "gov": true, "io": true, "co": true, "ca": true, "uk": true,
	"us": true, "de": true, "fr": true, "ai": true, "app": true, "dev": true, "tv": true, "me": true, "info": true, "biz": true, "au": true, "nz": true,
	"ie": true, "eu": true, "ch": true, "nl": true, "be": true, "es": true, "it": true, "se": true, "no": true, "dk": true, "fi": true, "jp": true,
	"in": true, "br": true, "mx": true, "xyz": true, "site": true, "online": true, "store": true, "tech": true, "studio": true, "media": true,
	"news": true, "blog": true, "video": true, "art": true, "design": true, "agency": true, "org.uk": true, "page": true, "live": true, "ly": true,
	"gg": true, "fm": true, "to": true, "cc": true, "at": true, "pl": true, "pt": true, "ph": true, "sg": true, "za": true, "kr": true}

// personLinks are the web addresses in what a person typed, as typed: with
// http(s):// or www., or a bare site with a common ending; never an email
// address, a file name or this computer's network.
func personLinks(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, ix := range linkInTextRe.FindAllStringIndex(text, -1) {
		raw := strings.TrimRight(text[ix[0]:ix[1]], ".,;:!?")
		if ix[0] > 0 && (text[ix[0]-1] == '@' || text[ix[0]-1] == '/' || text[ix[0]-1] == '.') {
			continue // an email address, or part of a longer path
		}
		if ix[1] < len(text) && (text[ix[1]] == '@') {
			continue
		}
		lower := strings.ToLower(raw)
		explicit := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "www.")
		u, err := url.Parse(map[bool]string{true: raw, false: "https://" + raw}[strings.Contains(raw, "://")])
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if !explicit {
			labels := strings.Split(host, ".")
			tld := labels[len(labels)-1]
			if len(labels) >= 3 && commonTLDs[labels[len(labels)-2]+"."+tld] {
				tld = labels[len(labels)-2] + "." + tld
			}
			if !commonTLDs[tld] {
				continue
			}
		}
		if localHost(host) && !resources.AllowLocalPages() {
			continue
		}
		if k := keep.LinkKey(raw); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, raw)
		}
	}
	return out
}

// localHost: this computer or a local network (never a source).
func localHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}

// linkKeysIn are the addresses a card's input names (any string field).
func linkKeysIn(input string) []string {
	if input == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(input), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch y := x.(type) {
		case string:
			if strings.Contains(y, ".") && !strings.ContainsAny(y, " \n") && len(y) < 2048 {
				if k := linkKey(y); k != "" {
					out = append(out, k)
				}
				if k := keep.LinkKey(y); k != "" {
					out = append(out, k)
				}
			}
		case []any:
			for _, e := range y {
				walk(e)
			}
		case map[string]any:
			for _, e := range y {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// projectLinks are the addresses a project's sources came from (pages,
// repositories, pictures and imported videos), compared by keep.LinkKey.
func (m *Manager) projectLinks(ctx context.Context, projectID string) map[string]bool {
	have := map[string]bool{}
	ws := m.App.WorkspaceID()
	var rs []struct {
		Versions []struct {
			Source models.ResourceSource `bson:"source"`
		} `bson:"versions"`
	}
	_ = m.App.DB.Find(ctx, store.CResources, ws, bson.M{"projectId": projectID, "removed": bson.M{"$ne": true}}, &rs,
		options.Find().SetProjection(bson.M{"versions.source.url": 1}).SetLimit(2000))
	for _, r := range rs {
		for _, v := range r.Versions {
			if v.Source.URL != "" {
				have[keep.LinkKey(v.Source.URL)] = true
			}
		}
	}
	var as []struct {
		Source *models.AssetSource `bson:"source"`
	}
	_ = m.App.DB.Find(ctx, store.CAssets, ws, bson.M{"projectIds": projectID, "source.url": bson.M{"$exists": true}}, &as,
		options.Find().SetProjection(bson.M{"source.url": 1}).SetLimit(2000))
	for _, x := range as {
		if x.Source != nil && x.Source.URL != "" {
			have[keep.LinkKey(x.Source.URL)] = true
		}
	}
	return have
}

/* ---------- A page's logo (D165) ---------- */

// offerSiteBrands asks to keep the logo a page snapshot found, once a card's
// snapshot has come in (the page's colours and font come with it).
func (m *Manager) offerSiteBrands(c *conv, cardIDs []string) {
	if len(cardIDs) == 0 {
		return
	}
	cards, err := c.cardsFor(cardIDs)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out []AssistantAction
	turn := 0
	for _, card := range cards {
		if card.Work == nil || card.Work.State != WorkFinished || len(card.Work.JobIDs) == 0 {
			continue
		}
		switch card.Op {
		case "resources.fetch", "keep.add", "add.links":
		default:
			continue
		}
		var js []models.Job
		if m.App.DB.Find(ctx, store.CJobs, m.App.WorkspaceID(), bson.M{"_id": bson.M{"$in": card.Work.JobIDs}, "type": jobs.TypeFetch}, &js) != nil {
			continue
		}
		for _, j := range js {
			for _, id := range stringsOf(j.Result["resourceIds"]) {
				if a := m.siteBrandCard(ctx, c, id); a != nil {
					turn = max(turn, card.Turn)
					out = append(out, *a)
				}
			}
		}
	}
	m.addCards(c, turn, out)
}

// siteBrandCard is the "Keep this logo?" card for what a card fetched: a
// website snapshot that found a logo (its colours and font come with it), or
// a picture that looks like a logo.
func (m *Manager) siteBrandCard(ctx context.Context, c *conv, resourceID string) *AssistantAction {
	var r models.Resource
	if err := m.App.DB.GetByID(ctx, store.CResources, m.App.WorkspaceID(), resourceID, &r); err != nil || r.Removed {
		return nil
	}
	project := r.ProjectID
	if project == "" {
		project = c.projectID()
	}
	v := r.Latest()
	if project == "" || v == nil {
		return nil
	}
	if r.Kind == models.ResourceImage || r.Kind == models.ResourceSVG {
		// A picture link brought in: a logo is offered for the brand kit.
		if !looksLikeLogo(r.Name+" "+v.Source.Name, v.MIME, v.Width, v.Height) {
			return nil
		}
		a := AssistantAction{ID: shortID("act"), Kind: "keep", State: ActionPending, CreatedAt: time.Now().UTC()}
		in := ops.KeepAddIn{ProjectID: project, Kind: models.KeepPicture, Text: r.Name, From: "metarush",
			Detail: models.KeepDetail{ResourceID: r.ID, BrandKit: true, Why: "It came in from " + linkShown(v.Source.URL) + "."}}
		if !m.offerKeep(ctx, c, &a, in, keepTitle(models.KeepPicture, in)) {
			return nil
		}
		a.Image = "/media/resources/" + r.ID + "/thumb"
		for _, col := range v.Colors {
			if len(a.Keeping.Colors) < 5 {
				a.Keeping.Colors = append(a.Keeping.Colors, col)
			}
		}
		return &a
	}
	site := v.Site
	if r.Kind != models.ResourceWebsite || site == nil || site.Brand == nil || len(site.Brand.Logos) == 0 {
		return nil
	}
	logo := site.Brand.Logos[0]
	a := AssistantAction{ID: shortID("act"), Kind: "keep", State: ActionPending, CreatedAt: time.Now().UTC()}
	in := ops.KeepAddIn{ProjectID: project, Kind: models.KeepPicture, Text: logo.Name, From: "metarush",
		Detail: models.KeepDetail{ResourceID: logo.ResourceID, FromSite: r.ID, BrandKit: true, URL: logo.URL, Why: "The snapshot of " + linkShown(site.URL) + " found it."}}
	if !m.offerKeep(ctx, c, &a, in, keepTitle(models.KeepPicture, in)) {
		return nil
	}
	a.Image = "/media/resources/" + logo.ResourceID + "/thumb"
	for _, col := range site.Brand.Colors {
		if len(a.Keeping.Colors) < 5 {
			a.Keeping.Colors = append(a.Keeping.Colors, col.Hex)
		}
	}
	a.Details = append(a.Details, "From "+linkShown(site.URL))
	return &a
}

/* ---------- Deciding ---------- */

// declineKept records a "Not now": the thing is never offered again in the project.
func declineKept(ctx context.Context, a *app.App, act AssistantAction) {
	if act.Keeping == nil || act.Keeping.ItemID == "" {
		return
	}
	it, err := keep.Get(ctx, a.DB, a.WorkspaceID(), act.Keeping.ItemID)
	if err != nil || it.State == models.KeepKept {
		return
	}
	_, _ = keep.Decide(ctx, a.DB, a.WorkspaceID(), *it, models.KeepDeclined, a.Actor, "")
}

/* ---------- What the project keeps, in every turn's context ---------- */

// memoryContext is what a project keeps, for Claude at the start of every
// turn: its rules (and which the quality check measures), the brief and where
// the video goes, sign-offs still waiting, and what was offered to keep before.
func memoryContext(ctx context.Context, a *app.App, p *models.Project) []string {
	if p == nil {
		return nil
	}
	var out []string
	var rs, cs, hs, hc, ps []string
	houseOf := ""
	for _, r := range ops.RulesFor(ctx, a, p) {
		s := "“" + r.Text + "”"
		if w := ops.MeasureWords(r.Measure); w != "" {
			s += " (the quality check measures it: " + w + ")"
		}
		switch {
		case r.Promise:
			// Promises made to the client (D300): broken, they hold the version back.
			if r.House {
				s += " (from “" + r.HouseOf + "”'s settings)"
			}
			ps = append(ps, s)
		case r.House && r.Check:
			hc, houseOf = append(hc, s), r.HouseOf
		case r.House:
			hs, houseOf = append(hs, s), r.HouseOf
		case r.Check:
			cs = append(cs, s)
		default:
			rs = append(rs, s)
		}
	}
	if len(ps) > 0 {
		out = append(out, "- Promises made to the client (keep them in everything you propose; a version that breaks one waits before it goes out, and only a person sends it out anyway): "+strings.Join(ps, "; "))
	}
	if len(rs) > 0 {
		out = append(out, "- This project's rules (follow them in everything you propose): "+strings.Join(rs, "; "))
	}
	if len(cs) > 0 {
		out = append(out, "- Problems the person doesn't want to see again (checked in every quality check): "+strings.Join(cs, "; "))
	}
	if len(hs) > 0 {
		out = append(out, "- House rules from “"+houseOf+"”'s settings (every project using them follows these; follow them too): "+strings.Join(hs, "; "))
	}
	if len(hc) > 0 {
		out = append(out, "- House checks from “"+houseOf+"”'s settings (checked in every quality check of every project using them): "+strings.Join(hc, "; "))
	}
	if s := ops.StyleContext(ctx, a, p.ID); s != "" {
		out = append(out, s)
	}
	if s := ops.PackContext(ctx, a, p.ID); s != "" {
		out = append(out, s) // the layout pack (D263)
	}
	b := p.Brief
	var brief []string
	if b.Goal != "" {
		brief = append(brief, "goal “"+b.Goal+"”")
	}
	if b.Audience != "" {
		brief = append(brief, "for “"+b.Audience+"”")
	}
	if b.Tone != "" {
		brief = append(brief, "tone “"+b.Tone+"”")
	}
	if len(b.Deliverables) > 0 {
		var ds []string
		for _, d := range b.Deliverables {
			ds = append(ds, d.Name+" ("+d.Preset+")")
		}
		brief = append(brief, "where it goes: "+strings.Join(ds, ", "))
	}
	if len(brief) > 0 {
		out = append(out, "- The brief: "+strings.Join(brief, "; "))
	}
	if open := ops.OpenSignoffs(ctx, a, p, ""); len(open) > 0 {
		var ss []string
		for _, s := range open {
			ss = append(ss, "“"+s.Text+"”")
		}
		out = append(out, "- Sign-offs still waiting before the video goes out (remind the person before approving or exporting): "+strings.Join(ss, "; "))
	}
	items, err := keep.List(ctx, a.DB, a.WorkspaceID(), p.ID, "")
	if err == nil && len(items) > 0 {
		var kept, declined, asked []string
		for _, it := range items {
			s := it.Kind + " “" + clip(orStr(it.Text, keepItemWords(it)), 80) + "”"
			switch it.State {
			case models.KeepKept:
				kept = append(kept, s)
			case models.KeepDeclined:
				declined = append(declined, s)
			default:
				asked = append(asked, s)
			}
		}
		line := "- Already offered to keep in this project (never offer these again): "
		var parts []string
		for _, x := range []struct {
			label string
			list  []string
		}{{"kept", kept}, {"the person said not now", declined}, {"waiting on a card", asked}} {
			if len(x.list) > 0 {
				parts = append(parts, x.label+": "+strings.Join(x.list[:min(12, len(x.list))], ", "))
			}
		}
		out = append(out, line+strings.Join(parts, "; "))
	}
	return out
}

// keepItemWords is a brief item in words.
func keepItemWords(it models.KeepItem) string {
	return keepShownText(ops.KeepAddIn{Kind: it.Kind, Text: it.Text, Detail: it.Detail})
}
