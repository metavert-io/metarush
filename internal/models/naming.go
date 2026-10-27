package models

import "time"

// Naming says who named a face or a voice as a person, and when (D274): a
// person, or software acting for one — an AI app, Claude's Auto, Claude, or
// MetaRush by itself. Consent checks count only the names a person gave.
type Naming struct {
	By    string    `bson:"by" json:"by" jsonschema:"enum=person,enum=agent,enum=auto,enum=claude,enum=machine"`
	Actor Actor     `bson:"actor" json:"actor"`
	At    time.Time `bson:"at" json:"at"`
}

// Who named a face or a voice.
const (
	NamedByPerson  = "person"  // a person: a dashboard click, a terminal, a card a person ran
	NamedByAgent   = "agent"   // an AI app through MCP or the command line
	NamedByAuto    = "auto"    // Claude's Auto ran it by itself
	NamedByClaude  = "claude"  // Claude in the dashboard's panel
	NamedByMachine = "machine" // MetaRush by itself (a worker, a watched folder)
)

// ByPerson reports a name a person gave.
func (n *Naming) ByPerson() bool { return n != nil && n.By == NamedByPerson }

// Words says who named it, for people ("an AI app (codex) on 25 Oct 2026").
func (n *Naming) Words() string {
	if n == nil {
		return "someone, before MetaRush recorded who names faces and voices"
	}
	who := ""
	switch n.By {
	case NamedByPerson:
		who = n.Actor.UserName
		if who == "" {
			who = "a person"
		}
	case NamedByAgent:
		who = "an AI app"
		if n.Actor.Agent != "" {
			who += " (" + n.Actor.Agent + ")"
		}
	case NamedByAuto:
		who = "Claude's Auto"
	case NamedByClaude:
		who = "Claude"
	default:
		who = "MetaRush by itself"
	}
	if n.At.IsZero() {
		return who
	}
	return who + " on " + n.At.Local().Format("2 Jan 2006")
}

// NamedByPerson reports whether a person named this voice: the name is a
// person's (not matched automatically) and the naming record says a person
// gave it (D274). A name from before the record was kept doesn't count.
func (v *AssetSpeaker) NamedByPerson() bool {
	return v.PersonID != "" && v.Source == SpeakerByPerson && v.Naming.ByPerson()
}

// NamedByPerson reports whether a person named this face (D274). A face named
// before the record was kept counts when it was confirmed in the dashboard.
func (f *FaceTrack) NamedByPerson() bool {
	if f.PersonID == "" {
		return false
	}
	if f.Naming != nil {
		return f.Naming.ByPerson()
	}
	return f.ConfirmedBy != nil && f.ConfirmedBy.Via == "dashboard" && f.ConfirmedBy.Agent == ""
}
