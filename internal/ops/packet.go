package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The one-page packet (D250): for an AI app's draft or any version, what
// changed, why (the maker's own words), what the checks found, what it costs
// and used, who made it and what's still waiting — to hand a draft to a
// client or a colleague. The dashboard shows it at /review/<id>; --json is
// this structure, and Markdown is the same page as text.

// PacketIn chooses the draft or version.
type PacketIn struct {
	ID     string `json:"id" jsonschema:"minLength=1,description=A draft (drf_…) or a version (its ID)."`
	Format string `json:"format,omitempty" jsonschema:"enum=,enum=json,enum=markdown,description=markdown: also the page as Markdown\\, to send to a client or a colleague."`
}

// Packet is the page.
type Packet struct {
	ID    string `json:"id"`
	Kind  string `json:"kind" jsonschema:"enum=version,enum=change"`
	Title string `json:"title"`
	// State: a draft's (waiting, approved, not_quite, turned_down,
	// replaced), else the version's approval (draft, approved, rejected).
	State string `json:"state"`
	// Status says it in words: "Waiting for your review", "Approved by Theo".
	Status      string           `json:"status"`
	ProjectID   string           `json:"projectId,omitempty"`
	ProjectName string           `json:"projectName,omitempty"`
	CutTitle    string           `json:"cutTitle,omitempty"`
	Revision    *RevisionSummary `json:"revision,omitempty"`
	Draft       *DraftView       `json:"draft,omitempty"`
	// InCut: the version is the cut's latest; a draft's isn't until approved.
	InCut bool      `json:"inCut"`
	Who   PacketWho `json:"who"`
	Why   []string  `json:"why" jsonschema:"description=The maker's own words: the note\\, what was asked\\, why."`
	// Changes says what changes, in plain words (a version: against the
	// version it was made from).
	Changes []string `json:"changes"`
	// SheetDiff is the cut sheet's lines that differ from the version it was
	// made from (Details: the words above say the same for people).
	SheetDiff string `json:"sheetDiff,omitempty"`
	// BaseRevisionID is the version it was made from (to compare with).
	BaseRevisionID string           `json:"baseRevisionId,omitempty"`
	Checks         PacketChecks     `json:"checks"`
	ReviewCopy     *DraftReviewCopy `json:"reviewCopy,omitempty"`
	Receipt        string           `json:"receipt"`
	// Waiting is what still waits: a person's OK, sign-offs, the review copy.
	Waiting  []string `json:"waiting"`
	Decision string   `json:"decision,omitempty" jsonschema:"description=Who decided what\\, and their note\\, in words."`
	URL      string   `json:"url" jsonschema:"description=This page in the dashboard."`
	// Markdown is the page as text (format markdown).
	Markdown string `json:"markdown,omitempty"`
}

// PacketWho is who made it.
type PacketWho struct {
	// Made says it in words: "Codex, through MetaRush's tools for AI apps,
	// working for Theo".
	Made      string    `json:"made"`
	Agent     string    `json:"agent,omitempty"`
	AgentName string    `json:"agentName,omitempty"`
	Via       string    `json:"via,omitempty"`
	Person    string    `json:"person,omitempty"`
	At        time.Time `json:"at"`
}

// PacketChecks is what MetaRush's checks found.
type PacketChecks struct {
	// Said is one line: "Nothing stops it; 1 thing worth checking."
	Said string `json:"said"`
	// Warnings are the change check's notes; Problems and Worth the review
	// copy's quality check (problems must be fixed, the rest may be fine).
	Warnings []string `json:"warnings"`
	Problems []string `json:"problems"`
	Worth    []string `json:"worthChecking"`
}

func init() {
	Def("review.packet", "One page for a draft or a version, to hand to a client or a colleague: what changed and why, the checks, the cost, who made it and what is still waiting (format markdown: also as Markdown)", Flags{},
		func(ctx context.Context, a *app.App, in *PacketIn) (*Packet, error) { return buildPacket(ctx, a, in) })
}

func buildPacket(ctx context.Context, a *app.App, in *PacketIn) (*Packet, error) {
	id := strings.TrimSpace(in.ID)
	var d *models.Draft
	if strings.HasPrefix(id, ids.Draft+"_") {
		dd, err := getDraft(ctx, a, id)
		if err != nil {
			return nil, err
		}
		d = dd
	} else {
		r, _, err := getRevision(ctx, a, id)
		if err != nil {
			return nil, err
		}
		if r.DraftID != "" {
			if dd, err := getDraft(ctx, a, r.DraftID); err == nil {
				d = dd
			}
		}
		if d == nil {
			return versionPacket(ctx, a, r, in.Format)
		}
	}
	return draftPacket(ctx, a, d, in.Format)
}

// draftPacket is the page of an AI app's draft.
func draftPacket(ctx context.Context, a *app.App, d *models.Draft, format string) (*Packet, error) {
	v := viewDraft(ctx, a, d)
	pk := &Packet{ID: d.ID, Kind: d.Kind, Title: v.Title, State: d.State, ProjectID: d.ProjectID, ProjectName: v.ProjectName, CutTitle: v.CutTitle,
		Revision: v.Revision, Draft: v, Changes: v.Changes, ReviewCopy: v.ReviewCopy, Receipt: v.Receipt, URL: v.URL, Waiting: []string{}}
	pk.Who = PacketWho{Agent: d.Agent, AgentName: v.AgentName, Via: d.CreatedBy.Via, Person: d.CreatedBy.UserName, At: d.CreatedAt}
	pk.Who.Made = madeWords(v.AgentName, v.ViaWords, d.CreatedBy.UserName)
	if d.Kind == models.DraftVersion && v.Revision != nil {
		pk.Title = fmt.Sprintf("Version %d of %s: %s", v.Revision.Number, q(v.ProjectName), lowerFirst(v.Title))
		pk.InCut = v.Revision.IsHead
	}
	if d.Note != "" {
		pk.Why = append(pk.Why, d.Note)
	}
	if v.Revision != nil {
		for _, s := range []string{v.Revision.Instruction, v.Revision.Rationale} {
			if s = strings.TrimSpace(s); s != "" && s != d.Note {
				pk.Why = append(pk.Why, s)
			}
		}
	}
	if pk.Why == nil {
		pk.Why = []string{}
	}
	pk.Checks = packetChecks(ctx, a, d.Warnings, v.ReviewCopy)
	if d.Kind == models.DraftVersion && d.RevisionID != "" {
		if r, _, err := getRevision(ctx, a, d.RevisionID); err == nil {
			pk.BaseRevisionID, pk.SheetDiff = sheetDiff(ctx, a, r)
		}
	}
	switch d.State {
	case models.DraftWaiting:
		pk.Status = "Waiting for your review"
		if d.Kind == models.DraftVersion {
			pk.Status += ": not in the cut until someone approves it"
		}
		pk.Waiting = append(pk.Waiting, "A person's OK: approve it, say Not quite with what should change, or turn it down.")
		if v.HeadMoved {
			pk.Waiting = append(pk.Waiting, "Someone saved a newer version since: approving keeps both changes where they don't overlap.")
		}
		if d.Tried != nil {
			pk.Waiting = append(pk.Waiting, "The last approval couldn't be carried out: "+d.Tried.Message)
		}
	case models.DraftNotQuite:
		pk.Status = "Not quite: waiting for " + v.AgentName + "'s next draft"
	case models.DraftApproved:
		pk.Status = "Approved"
	case models.DraftTurnedDown:
		pk.Status = "Turned down"
	case models.DraftReplaced:
		pk.Status = "A newer draft took its place"
	case models.DraftApproving:
		pk.Status = "Being approved"
	case models.DraftInCut:
		pk.Status = "In the cut: someone saved a change on top of it"
	}
	if dec := d.Decision; dec != nil {
		pk.Decision = decisionWords(dec)
		if dec.By.UserName != "" && (d.State == models.DraftApproved || d.State == models.DraftTurnedDown) {
			pk.Status += " by " + dec.By.UserName
		}
	}
	if d.Kind == models.DraftVersion {
		pk.Waiting = append(pk.Waiting, reviewCopyWaiting(v.ReviewCopy)...)
		if p := projectOf(ctx, a, d.ProjectID); p != nil && d.State != models.DraftTurnedDown && d.State != models.DraftReplaced {
			for _, s := range OpenSignoffs(ctx, a, p, firstNonEmpty(d.Branch, "main")) {
				pk.Waiting = append(pk.Waiting, "Sign-off: "+q(s.Text)+".")
			}
		}
	}
	if format == "markdown" {
		pk.Markdown = packetMarkdown(pk)
	}
	return pk, nil
}

// versionPacket is the page of a version no draft holds.
func versionPacket(ctx context.Context, a *app.App, r *models.Revision, format string) (*Packet, error) {
	p := projectOf(ctx, a, r.ProjectID)
	if p == nil {
		return nil, apperr.New(apperr.NotFound, "The project of that version isn't in this workspace any more.")
	}
	head := p.Branches[r.Branch].Head
	s := summarizeRev(a, *r, head)
	pk := &Packet{ID: r.ID, Kind: models.DraftVersion, State: r.Approval.State, ProjectID: p.ID, ProjectName: p.Name, Revision: &s, InCut: r.ID == head,
		URL: dashboardURL(a, "/review/"+r.ID), Waiting: []string{}, Why: []string{}}
	if len(p.Branches) > 1 {
		pk.CutTitle = p.Branches[r.Branch].Title
	}
	pk.Title = fmt.Sprintf("Version %d of %s", r.Number, q(p.Name))
	if r.Note != "" {
		pk.Title += ": " + r.Note
	}
	agent := madeByAgent(r)
	name := ""
	if agent != "" {
		name = AgentWords(agent)
	}
	pk.Who = PacketWho{Agent: agent, AgentName: name, Via: r.CreatedBy.Via, Person: r.CreatedBy.UserName, At: r.CreatedAt}
	pk.Who.Made = madeWords(name, ViaWords(r.CreatedBy.Via), r.CreatedBy.UserName)
	for _, t := range []string{r.Instruction, r.Rationale} {
		if t = strings.TrimSpace(t); t != "" {
			pk.Why = append(pk.Why, t)
		}
	}
	parent := &editspec.Spec{Output: r.Spec.Output}
	if r.ParentID != "" {
		var pr models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r.ParentID, &pr) == nil {
			parent = &pr.Spec
		}
	}
	info := newSheetInfo(ctx, a)
	pk.Changes = cutsheet.Describe(parent, &r.Spec, cutsheet.NameFunc(&r.Spec, info, projectAssetIDs(ctx, a, p.ID)))
	var warnings []string
	for _, w := range rewordWarnings(ctx, a, r) {
		warnings = append(warnings, withoutCommands(w.Message))
	}
	pk.ReviewCopy = draftReviewCopy(ctx, a, r.ID)
	pk.Checks = packetChecks(ctx, a, warnings, pk.ReviewCopy)
	pk.BaseRevisionID, pk.SheetDiff = sheetDiff(ctx, a, r)
	pk.Receipt = versionReceipt(pk.ReviewCopy)
	switch r.Approval.State {
	case models.ApprovalApproved:
		pk.Status = "Approved"
	case models.ApprovalRejected:
		pk.Status = "Needs changes"
	default:
		pk.Status = "Draft"
		pk.Waiting = append(pk.Waiting, "A person's OK: approve it or say what should change.")
	}
	if r.Approval.By != nil && r.Approval.By.UserName != "" && r.Approval.State != models.ApprovalDraft {
		pk.Status += " by " + r.Approval.By.UserName
		pk.Decision = fmt.Sprintf("%s by %s, %s.", pk.Status[:strings.Index(pk.Status, " by ")], r.Approval.By.UserName, packetTime(*r.Approval.At))
		if r.Approval.Note != "" {
			pk.Decision += " " + q(r.Approval.Note)
		}
	}
	if pk.InCut {
		pk.Status += " (the latest version)"
	}
	pk.Waiting = append(pk.Waiting, reviewCopyWaiting(pk.ReviewCopy)...)
	if pk.InCut {
		for _, so := range OpenSignoffs(ctx, a, p, r.Branch) {
			pk.Waiting = append(pk.Waiting, "Sign-off: "+q(so.Text)+".")
		}
	}
	if format == "markdown" {
		pk.Markdown = packetMarkdown(pk)
	}
	return pk, nil
}

func projectOf(ctx context.Context, a *app.App, id string) *models.Project {
	if id == "" {
		return nil
	}
	var p models.Project
	if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p) != nil {
		return nil
	}
	return &p
}

// madeWords: "Codex, through MetaRush's tools for AI apps, working for Theo";
// a person's own: "Theo".
func madeWords(agentName, via, person string) string {
	if agentName == "" {
		if person == "" {
			return "Someone"
		}
		return person
	}
	s := agentName
	if via != "" {
		s += ", " + via
	}
	if person != "" {
		s += ", working for " + person
	}
	return s
}

func versionReceipt(rc *DraftReviewCopy) string {
	s := "Costs nothing to approve: no outside service is paid."
	if rc != nil && rc.State == models.RenderAvailable {
		u := "a review copy"
		if rc.Machine != "" {
			u += " made on " + q(rc.Machine)
		}
		s += " Used " + u + " and its quality check."
	}
	return s
}

func reviewCopyWaiting(rc *DraftReviewCopy) []string {
	switch {
	case rc == nil:
		return []string{"The review copy: MetaRush makes one by itself about a minute after the change."}
	case rc.State == models.RenderAvailable && rc.Problems > 0:
		return []string{"The review copy's quality check found " + plural(rc.Problems, "problem") + " to look at."}
	case rc.State == models.RenderAvailable:
		return nil
	case rc.State == models.RenderFailed:
		return []string{"The review copy couldn't be made: ask for it again from the version's page."}
	}
	return []string{"The review copy is being made."}
}

// packetChecks gathers what the change's check and the review copy's
// quality check found.
func packetChecks(ctx context.Context, a *app.App, warnings []string, rc *DraftReviewCopy) PacketChecks {
	c := PacketChecks{Warnings: warnings, Problems: []string{}, Worth: []string{}}
	if c.Warnings == nil {
		c.Warnings = []string{}
	}
	if rc != nil && rc.State == models.RenderAvailable {
		var r models.Render
		if a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rc.RenderID, &r) == nil && r.QA != nil && r.QA.ReportID != "" {
			var rep models.QAReport
			if a.DB.C(store.CQAReports).FindOne(ctx, bson.M{"_id": r.QA.ReportID, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"findings": 1})).Decode(&rep) == nil {
				for _, f := range rep.Findings {
					line := f.Message
					if f.Timecode != "" {
						line = f.Timecode + ": " + line
					}
					switch f.Severity {
					case "error":
						c.Problems = append(c.Problems, line)
					case "warning":
						c.Worth = append(c.Worth, line)
					}
				}
			}
		}
	}
	parts := []string{"Nothing stops it"}
	if n := len(c.Warnings) + len(c.Worth); n > 0 {
		parts = append(parts, plural(n, "thing")+" worth checking")
	}
	if len(c.Problems) > 0 {
		parts[0] = "The review copy's quality check found " + plural(len(c.Problems), "problem")
	}
	if rc == nil || rc.State != models.RenderAvailable {
		parts = append(parts, "the review copy isn't checked yet")
	}
	c.Said = strings.Join(parts, "; ") + "."
	return c
}

func decisionWords(dec *models.DraftDecision) string {
	who := dec.By.UserName
	if who == "" {
		who = "Someone"
	}
	var s string
	switch dec.State {
	case models.DraftApproved:
		s = who + " approved it"
	case models.DraftNotQuite:
		s = who + " said Not quite"
	case models.DraftTurnedDown:
		s = who + " turned it down"
	case models.DraftInCut:
		s = "Its change reached the cut"
	}
	s += ", " + packetTime(dec.At) + "."
	if dec.Note != "" {
		s += " " + q(dec.Note)
	}
	if dec.Result != "" {
		s += " " + dec.Result
	}
	return s
}

func packetTime(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") }

// packetMarkdown is the page as Markdown. Every word in it is escaped, so an
// app's words (or anyone's) can't become links, pictures or HTML.
func packetMarkdown(pk *Packet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", mdText(pk.Title))
	fmt.Fprintf(&b, "**%s.** Made by %s, %s.", mdText(pk.Status), mdText(pk.Who.Made), packetTime(pk.Who.At))
	if pk.CutTitle != "" {
		fmt.Fprintf(&b, " Cut: %s.", mdText(q(pk.CutTitle)))
	}
	b.WriteString("\n\n## What changes\n\n")
	for _, c := range pk.Changes {
		fmt.Fprintf(&b, "- %s\n", mdText(c))
	}
	if len(pk.Changes) == 0 {
		b.WriteString("- Nothing.\n")
	}
	if len(pk.Why) > 0 {
		b.WriteString("\n## Why\n\n")
		for i, w := range pk.Why {
			if i > 0 {
				b.WriteString(">\n")
			}
			for _, line := range strings.Split(strings.TrimSpace(w), "\n") {
				fmt.Fprintf(&b, "> %s\n", mdText(line))
			}
		}
	}
	b.WriteString("\n## Checks\n\n")
	fmt.Fprintf(&b, "%s\n", mdText(pk.Checks.Said))
	for _, l := range pk.Checks.Problems {
		fmt.Fprintf(&b, "- Problem: %s\n", mdText(l))
	}
	for _, l := range append(append([]string{}, pk.Checks.Warnings...), pk.Checks.Worth...) {
		fmt.Fprintf(&b, "- Worth checking: %s\n", mdText(l))
	}
	if rc := pk.ReviewCopy; rc != nil {
		fmt.Fprintf(&b, "\nReview copy: %s.\n", mdText(lowerFirst(rc.Said)))
	}
	b.WriteString("\n## Cost\n\n" + mdText(pk.Receipt) + "\n")
	b.WriteString("\n## What's waiting\n\n")
	for _, w := range pk.Waiting {
		fmt.Fprintf(&b, "- %s\n", mdText(w))
	}
	if len(pk.Waiting) == 0 {
		b.WriteString("- Nothing.\n")
	}
	if pk.Decision != "" {
		b.WriteString("\n## Decision\n\n" + mdText(pk.Decision) + "\n")
	}
	fmt.Fprintf(&b, "\n[Open it in MetaRush](<%s>)\n", strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(pk.URL))
	return b.String()
}

// mdText escapes text for Markdown: what would make a link (a web address
// too), a picture, code, emphasis or HTML is shown as written instead.
func mdText(s string) string {
	s = cleanText(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '|', '~':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '\n':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("://", "\\://", "www.", "www\\.").Replace(b.String())
}

// sheetDiff is a version's cut sheet lines that differ from the version it
// was made from (none for a first cut).
func sheetDiff(ctx context.Context, a *app.App, r *models.Revision) (string, string) {
	if r.ParentID == "" {
		return "", ""
	}
	var parent models.Revision
	if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r.ParentID, &parent) != nil {
		return "", ""
	}
	info := newSheetInfo(ctx, a)
	return parent.ID, cutsheet.Unified(cutsheet.LineDiff(printRevision(ctx, a, info, &parent, false), printRevision(ctx, a, info, r, false)), 2)
}
