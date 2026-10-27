package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/ops"
)

// Layout packs in Claude's panel (D266). When Claude proposes a pack:
// it writes the custom pieces as project motion graphics (each checked by a
// card or a safe step, then approved by a person under Motion graphics) and
// the pack as a JSON file in the conversation's proposals folder, and asks
// `metarush content pack propose --file …`. The refused command becomes a
// card that only a person decides (never Auto): accepting it makes the pack
// current once every graphic in it is approved, and keeps it as the
// settings' proposed pack until then. `content pack set` (vetted templates
// only), `accept` and `decline` are cards too; `content pack apply` is a cut
// edit card (cutedits.go).

// maxPackFile is the largest pack file Claude may write.
const maxPackFile = 1 << 20

// packCommands are the layout pack commands that become cards here.
var packCommands = map[string]bool{"content pack propose": true, "content pack set": true, "content pack accept": true, "content pack decline": true}

// packOp calls a layout pack operation for a card's preview, as MetaRush
// (never as Claude, never as a person).
func packOp(ctx context.Context, m *Manager, name string, in any) (*ops.ContentPackOut, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	res, err := ops.Call(ctx, m.App, name, raw, ops.Caller{})
	if err != nil {
		return nil, err
	}
	out, ok := res.(*ops.ContentPackOut)
	if !ok {
		return nil, fmt.Errorf("unexpected answer")
	}
	return out, nil
}

func (m *Manager) describePack(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	project := strings.TrimSpace(call.Flags["project"])
	if project == "" && c != nil {
		project = c.projectID()
	}
	switch call.Path {
	case "content pack propose":
		m.describePackPropose(ctx, c, call, a, project)
	case "content pack accept":
		cur, err := packOp(ctx, m, "content.pack.show", ops.ContentPackIn{ProjectID: project})
		if err != nil || cur.Draft == nil {
			incomplete(a, "Use the proposed layout pack", "No layout pack is proposed for these settings.")
			return
		}
		a.Title = "Use the proposed layout pack"
		a.Details = append([]string{}, cur.Draft.Lines...)
		if !cur.Draft.Ready {
			a.Details = append(a.Details, cur.Draft.Why)
		}
		a.HumanOnly = true
		a.Review = links.ContentSettings(cur.ProjectID, "pack")
		at := cur.Draft.At // the proposed pack shown here, not one proposed after
		setInput(a, "content.pack.accept", ops.ContentPackAcceptIn{ProjectID: project, DraftAt: &at})
	case "content pack decline":
		a.Title = "Set the proposed layout pack aside"
		a.Details = []string{"The pack stays as it is."}
		setInput(a, "content.pack.decline", ops.ContentPackDeclineIn{ProjectID: project, Note: clip(call.Flags["note"], 500)})
	case "content pack set":
		m.describePackSet(ctx, call, a, project)
	}
}

// describePackPropose is a pack Claude proposes: what each part is drawn
// with, the graphics still waiting for a person, and one decision.
func (m *Manager) describePackPropose(ctx context.Context, c *conv, call *cliCall, a *AssistantAction, project string) {
	const title = "Use Claude's layout pack"
	file := strings.TrimSpace(call.Flags["file"])
	if file == "" {
		incomplete(a, title, "Claude did not give the pack's file (--file in this conversation's proposals folder).")
		return
	}
	raw, why := m.proposalJSON(c, file)
	if why != "" {
		incomplete(a, title, why)
		return
	}
	var pack editspec.LayoutPack
	var wrapped struct {
		Pack *editspec.LayoutPack `json:"pack"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Pack != nil {
		pack = *wrapped.Pack
	} else {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&pack); err != nil {
			incomplete(a, title, "Claude's pack isn't written the way MetaRush reads it.")
			return
		}
	}
	note := clip(strings.TrimSpace(call.Flags["note"]), 500)
	origin := ""
	if c != nil {
		origin = "assistant:" + c.id
	}
	prev, err := packOp(ctx, m, "content.pack.propose", ops.ContentPackProposeIn{ProjectID: project, Pack: pack, Note: note, Origin: origin, DryRun: true})
	if err != nil {
		unavailable(a, title, "MetaRush can't use this pack: "+firstLine(apperr.From(err).Message))
		return
	}
	d := prev.Draft
	if d == nil {
		unavailable(a, title, "MetaRush couldn't read the pack.")
		return
	}
	// The card carries the pack as checked now — its shared libraries pinned —
	// and that exact pack is what a person accepts.
	pack = d.Pack
	name := strings.TrimSpace(pack.Name)
	if name != "" {
		a.Title = clip(fmt.Sprintf("Use “%s” as the layout pack of %s's settings", name, theirs(prev.Project)), 160)
	} else {
		a.Title = clip(fmt.Sprintf("Use Claude's layout pack for %s's settings", theirs(prev.Project)), 160)
	}
	a.Details = append([]string{}, d.Lines...)
	if len(d.Lines) == 0 {
		a.Details = []string{"MetaRush's own graphics for every part, in the house style."}
	}
	if note != "" {
		a.Details = append(a.Details, "Claude's note: "+theirs(note))
	}
	if len(d.Waiting) > 0 {
		var names []string
		for _, w := range d.Waiting {
			names = append(names, w.Name)
		}
		a.Details = append(a.Details, "Waits for a person to approve "+strings.Join(names, " and ")+" under Motion graphics; until then accepting keeps it as the proposed pack.")
		a.Review = d.Waiting[0].Review
	} else {
		a.Review = links.ContentSettings(prev.ProjectID, "pack")
	}
	if prev.UsedBy > 0 {
		a.Details = append(a.Details, fmt.Sprintf("These settings are also used by %s.", pluralize(prev.UsedBy, "project")))
	}
	a.Details = append(a.Details, "New cuts draw with it; the latest cuts are offered it, and saved versions don't change.")
	a.HumanOnly = true // a pack with code is a person's decision, never Auto's
	// Accepting it after the pack changed is refused: the card says what it replaces as it is now.
	base := prev.Version
	setInput(a, "content.pack.accept", ops.ContentPackAcceptIn{ProjectID: project, Pack: &pack, Note: note, Origin: origin, BaseVersion: &base})
}

// describePackSet is a change to the pack with vetted templates only (code
// reaches a pack only through a proposal a person accepts).
func (m *Manager) describePackSet(ctx context.Context, call *cliCall, a *AssistantAction, project string) {
	const title = "Change the layout pack"
	if strings.TrimSpace(call.Flags["motion"]) != "" {
		unavailable(a, title, "A motion graphic goes into a layout pack through a proposed pack a person accepts: Claude proposes the whole pack instead.")
		return
	}
	in := ops.ContentPackSetIn{ProjectID: project, Role: strings.TrimSpace(call.Flags["role"]), Template: strings.TrimSpace(call.Flags["template"]), Reset: hasBool(call, "reset")}
	if v, ok := call.Flags["name"]; ok {
		v = clip(v, 80)
		in.Name = &v
	}
	if f, ok := call.Flags["fields"]; ok {
		if !json.Valid([]byte(f)) {
			incomplete(a, title, "Claude's choices for the graphic aren't a {name: value} list.")
			return
		}
		in.Fields = json.RawMessage(f)
	}
	if s, ok := call.Flags["seconds"]; ok {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			incomplete(a, title, "How long the graphic stays is a number of seconds.")
			return
		}
		in.Seconds = &v
	}
	if _, ok := call.Flags["logo"]; ok {
		v := call.Flags["logo"] != "false"
		in.Logo = &v
	}
	for flag, dst := range map[string]**string{"pip-corner": &in.PipCorner, "pip-size": &in.PipSize, "pip-shape": &in.PipShape, "pip-frame": &in.PipFrame,
		"gap": &in.Gap, "frame": &in.Frame, "backdrop": &in.Backdrop} {
		if v, ok := call.Flags[flag]; ok {
			v := v
			*dst = &v
		}
	}
	if _, ok := call.Flags["rounded"]; ok {
		v := call.Flags["rounded"] != "false"
		in.Rounded = &v
	}
	in.Remove = call.Multi["remove"]
	cur, err := packOp(ctx, m, "content.pack.show", ops.ContentPackIn{ProjectID: project})
	if err != nil {
		unavailable(a, title, firstLine(apperr.From(err).Message))
		return
	}
	if cur.ReadOnly {
		unavailable(a, title, fmt.Sprintf("The project uses %s's settings: their pack changes there.", theirs(cur.Project)))
		return
	}
	a.Title = clip(fmt.Sprintf("Change the layout pack of %s's settings", theirs(cur.Project)), 160)
	switch {
	case in.Reset:
		a.Details = []string{"Back to MetaRush's own pack, in the house style."}
	case in.Role != "" && in.Template != "":
		a.Details = []string{fmt.Sprintf("The %s is drawn with the %s template.", editspec.RoleWords(roleWords(in.Role)), editspec.TemplateWords(in.Template))}
	case in.Role != "":
		a.Details = []string{fmt.Sprintf("Changes how the pack draws the %s.", editspec.RoleWords(roleWords(in.Role)))}
	default:
		a.Details = []string{"Changes the pack's name or how call layouts look."}
	}
	if cur.UsedBy > 0 {
		a.Details = append(a.Details, fmt.Sprintf("These settings are also used by %s.", pluralize(cur.UsedBy, "project")))
	}
	setInput(a, "content.pack.set", in)
}

func roleWords(s string) string {
	if k, ok := editspec.ParseRoleWords(s); ok {
		return k
	}
	return s
}

// proposalJSON reads a JSON file Claude wrote in this conversation's
// proposals folder, and nowhere else. why says why it can't be used.
func (m *Manager) proposalJSON(c *conv, from string) (json.RawMessage, string) {
	if c == nil || from == "-" {
		return nil, "Claude's pack is not in this conversation's proposals folder."
	}
	p := from
	if !filepath.IsAbs(p) {
		p = filepath.Join(m.workspaceDir(), p)
	}
	real := func(s string) string {
		if rs, err := filepath.EvalSymlinks(s); err == nil {
			return rs
		}
		return s
	}
	root, file := real(filepath.Join(m.convDir(c.id), "proposals")), real(p)
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, "Claude's pack is not in this conversation's proposals folder."
	}
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxPackFile {
		return nil, "MetaRush can't read Claude's pack (a file of at most 1 MB in the proposals folder)."
	}
	b, err := os.ReadFile(file)
	if err != nil || !json.Valid(b) {
		return nil, "Claude's pack isn't a JSON file MetaRush can read."
	}
	return json.RawMessage(b), ""
}
