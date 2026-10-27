package assistant

import (
	"strings"
	"time"
)

// Whose Claude answers (D239). The Claude Code panel runs the host's Claude
// Code — the computer that holds the conversation — signed in to the host's
// Claude account: everyone who uses the panel on that MetaRush is answered by
// that account, under its terms. Settings → Claude Code and every
// conversation say whose account it is and what its terms say about the data
// Claude reads (transcripts, frames, messages), from Claude Code's own
// `auth status`; nothing is assumed when Claude Code doesn't say.

// Data terms of a Claude account.
const (
	TermsConsumer   = "consumer"   // Claude Free, Pro or Max: Anthropic's consumer terms
	TermsCommercial = "commercial" // Claude Team or Enterprise, an API key: Anthropic's commercial terms
	TermsCloud      = "cloud"      // Claude through Amazon Bedrock, Google Vertex AI or Microsoft Foundry
	TermsUnknown    = "unknown"
)

// ClaudeAccount is whose Claude account answers in the panel, and its terms.
type ClaudeAccount struct {
	// Machine is the computer whose Claude Code answers (the host).
	Machine      string `bson:"machine" json:"machine"`
	Email        string `bson:"email,omitempty" json:"email,omitempty" jsonschema:"description=The account Claude Code is signed in to\\, when it says."`
	Organization string `bson:"organization,omitempty" json:"organization,omitempty"`
	// Plan is how it is signed in: pro, max, team, enterprise, free, plan (a
	// Claude plan it didn't name), api, bedrock, vertex, foundry or unknown.
	Plan     string `bson:"plan" json:"plan"`
	PlanName string `bson:"planName" json:"planName" jsonschema:"description=The plan in words: Claude Max\\, an Anthropic API key."`
	Terms    string `bson:"terms" json:"terms" jsonschema:"enum=consumer,enum=commercial,enum=cloud,enum=unknown"`
	// TermsName and DataUse say the terms and what they mean for what Claude reads.
	TermsName string `bson:"termsName" json:"termsName"`
	DataUse   string `bson:"dataUse" json:"dataUse"`
	// Who says it in one line: "Claude Code on Studio Mac, signed in as host@example.com (Claude Max)".
	Who string    `bson:"who" json:"who"`
	At  time.Time `bson:"at,omitempty" json:"at,omitempty" jsonschema:"description=When it was seen (a conversation: its latest message)."`
}

// accountOf reads Claude Code's `auth status` (its JSON, loosely: fields
// differ between versions) for the computer named machine.
func accountOf(raw map[string]any, method, machine string) *ClaudeAccount {
	a := &ClaudeAccount{Machine: machine, Email: findString(raw, "email", "emailaddress", "accountemail", "useremail"),
		Organization: findString(raw, "orgname", "organizationname", "organization")}
	provider := strings.ToLower(findString(raw, "apiprovider", "provider"))
	sub := strings.ToLower(findString(raw, "subscriptiontype", "subscription", "plan", "accounttype"))
	m := strings.ToLower(method)
	has := func(s string, words ...string) bool {
		for _, w := range words {
			if strings.Contains(s, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has(provider+" "+m, "bedrock"):
		a.Plan, a.PlanName = "bedrock", "Amazon Bedrock"
	case has(provider+" "+m, "vertex"):
		a.Plan, a.PlanName = "vertex", "Google Vertex AI"
	case has(provider+" "+m, "foundry"):
		a.Plan, a.PlanName = "foundry", "Microsoft Foundry"
	case has(m, "claude.ai", "subscription", "oauth"):
		switch {
		case has(sub, "enterprise"):
			a.Plan, a.PlanName = "enterprise", "Claude Enterprise"
		case has(sub, "team"):
			a.Plan, a.PlanName = "team", "Claude Team"
		case has(sub, "max"):
			a.Plan, a.PlanName = "max", "Claude Max"
		case has(sub, "pro"):
			a.Plan, a.PlanName = "pro", "Claude Pro"
		case has(sub, "free"):
			a.Plan, a.PlanName = "free", "Claude Free"
		default:
			a.Plan, a.PlanName = "plan", "a Claude plan"
		}
	case has(m, "api", "key"):
		a.Plan, a.PlanName = "api", "an Anthropic API key"
	default:
		a.Plan, a.PlanName = "unknown", "a sign-in Claude Code didn't name"
	}
	switch a.Plan {
	case "free", "pro", "max", "plan":
		a.Terms, a.TermsName = TermsConsumer, "Anthropic's consumer terms"
		a.DataUse = "Under Anthropic's consumer terms, whether what Claude reads here — transcripts, frames, your messages — may be used to improve Claude follows this account's privacy settings at claude.ai. A Claude Team or Enterprise account, or an API key, is under Anthropic's commercial terms instead."
		if a.Plan == "plan" {
			a.DataUse += " Claude Code didn't say which plan this is: a Team or Enterprise plan is under the commercial terms."
		}
	case "team", "enterprise", "api":
		a.Terms, a.TermsName = TermsCommercial, "Anthropic's commercial terms"
		a.DataUse = "Under Anthropic's commercial terms, what Claude reads here — transcripts, frames, your messages — isn't used to train its models."
	case "bedrock", "vertex", "foundry":
		a.Terms, a.TermsName = TermsCloud, a.PlanName+"'s terms"
		a.DataUse = "Claude runs through " + a.PlanName + ", under its terms for Claude: what Claude reads here — transcripts, frames, your messages — isn't used to train models."
	default:
		a.Terms, a.TermsName = TermsUnknown, "terms MetaRush can't tell"
		a.DataUse = "Claude Code didn't say how it is signed in, so MetaRush can't say which terms apply to what Claude reads here. Check Claude Code's sign-in on " + machineWords(machine) + "."
	}
	a.Who = "Claude Code on " + machineWords(machine)
	switch {
	case a.Email != "":
		a.Who += ", signed in as " + a.Email + " (" + a.PlanName + ")"
	default:
		a.Who += " (" + a.PlanName + ")"
	}
	return a
}

func machineWords(machine string) string {
	if strings.TrimSpace(machine) == "" {
		return "this computer"
	}
	return machine
}

// findString finds the first string under one of the keys (compared without
// case and punctuation), at any depth.
func findString(v any, keys ...string) string {
	norm := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	want := map[string]int{}
	for i, k := range keys {
		want[k] = i
	}
	best, bestRank := "", len(keys)
	var walk func(x any, depth int)
	walk = func(x any, depth int) {
		if depth > 4 {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			for k, val := range t {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					if r, ok := want[norm(k)]; ok && r < bestRank {
						best, bestRank = strings.TrimSpace(s), r
					}
				}
				walk(val, depth+1)
			}
		case []any:
			for _, e := range t {
				walk(e, depth+1)
			}
		}
	}
	walk(v, 0)
	return best
}
