package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Abilities from community catalogues in the Claude panel (D486): Claude
// browses what the trusted catalogues list (`metarush ability browse`, read
// only, their words quoted as data) and may propose bringing one in. Its
// refused `metarush ability import --from-catalogue <catalogue>:<name>`
// becomes a card a person runs: the listing as the catalogue says it, pinned
// to the version Claude saw, the note that MetaRush didn't check it, and what
// happens after (a check on this computer, an owner's approval). It is a
// person's decision (HumanOnly), so Auto never runs it and it is never
// approved with others. Trusting a catalogue is never a card: it is an
// owner's choice under Settings → Abilities; nor is bringing in a file.

// abilityCommands are the commands handled here.
var abilityCommands = map[string]bool{"ability import": true, "ability catalogues add": true, "ability catalogues remove": true}

func (m *Manager) describeAbility(ctx context.Context, call *cliCall, a *AssistantAction) {
	switch call.Path {
	case "ability catalogues add", "ability catalogues remove":
		unavailable(a, "Change the catalogues of abilities this workspace trusts", "Which catalogues of abilities this workspace trusts is an owner's choice, under Settings → Abilities.")
		return
	}
	from := strings.TrimSpace(call.Flags["from-catalogue"])
	if from == "" {
		unavailable(a, "Bring in an ability file", "Bring in an ability file yourself, under Library → Abilities (Bring in an ability file…).")
		return
	}
	key, name, version, err := abilities.ParseListedRef(from)
	if err != nil {
		incomplete(a, "Bring in an ability", "Claude did not name a catalogue's ability as catalogue:name.")
		return
	}
	b, err := abilityOp[abilities.AbilityBrowseOut](ctx, m, "ability.browse", abilities.AbilityBrowseIn{Name: key + ":" + name})
	if err != nil {
		incomplete(a, "Bring in an ability", apperr.From(err).Message)
		return
	}
	if len(b.Abilities) == 0 {
		incomplete(a, "Bring in an ability", fmt.Sprintf("No catalogue this workspace trusts lists %s.", name))
		return
	}
	l := b.Abilities[0]
	if version != "" && !strings.HasPrefix(l.Entry.Archive, version) {
		incomplete(a, "Bring in an ability", "The catalogue lists another version of it now; look at it again.")
		return
	}
	a.Title = fmt.Sprintf("Bring in “%s” from “%s”", clip(l.Entry.Title, 80), clip(l.CatalogueTitle, 80))
	a.HumanOnly = true
	a.Review = abilityBrowseLink(l.Catalogue, l.Entry.Name)
	a.Details = []string{"“" + clip(l.Entry.Does, 300) + "”"}
	if l.Publisher != "" {
		a.Details = append(a.Details, "Listed by “"+clip(l.Publisher, 120)+"”")
	}
	a.Details = append(a.Details, "What it reaches: "+l.Reaches, "What leaves this computer when it runs: "+l.Leaves)
	a.Details = append(a.Details, l.Needs...)
	a.Details = append(a.Details, "Listed, not checked by MetaRush: it comes in as a draft, is checked on this computer, and an owner approves it before anyone runs it")
	switch {
	case l.Here != nil:
		unavailable(a, a.Title, fmt.Sprintf("This workspace has it already (version %d, %s).", l.Here.Number, l.Here.StateLabel))
		return
	case !l.CanBringIn:
		unavailable(a, a.Title, l.WhyNot)
		return
	}
	in := abilities.AbilityCatalogueImportIn{From: l.Catalogue + ":" + l.Entry.Name + "@" + l.Entry.Archive, AsVersion: hasBool(call, "as-version")}
	if l.Other != nil && !in.AsVersion {
		a.Details = append(a.Details, fmt.Sprintf("Another version of it is here (version %d): it comes in as its next version when it came from this catalogue", l.Other.Number))
	}
	setInput(a, "ability.catalogues.import", in)
}

// abilityBrowseLink opens a catalogue's listing in Library → Abilities.
func abilityBrowseLink(catalogue, name string) string {
	q := url.Values{"browse": {catalogue}, "listing": {name}}
	return "/library/abilities?" + q.Encode()
}

// abilityOp calls an abilities operation by name and reads its answer.
func abilityOp[T any](ctx context.Context, m *Manager, name string, in any) (*T, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	res, err := ops.Call(ctx, m.App, name, raw, ops.Caller{})
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
