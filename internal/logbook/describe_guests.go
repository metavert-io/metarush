package logbook

import "strings"

// Plain phrases for guests (D490–D504): the owners' operations, the guest
// API's operations, and every guest request recorded (D497). {v} in a guest
// phrase is the version as owners read it ("version 3 of “Spring promo”").
var guestPhrases = map[string]string{
	"guest.page":           "opened their guest page",
	"guest.signin":         "signed in with a sign-in link",
	"guest.signin.sso":     "signed in with their organization's account",
	"guest.signin.refused": "tried to sign in",
	"guest.signout":        "signed out",
	"guest.shared":         "looked at what was shared with them",
	"guest.watch":          "opened {v}",
	"guest.media":          "watched {v}",
	"guest.comments":       "read their comments on {v}",
	"guest.comment":        "commented on {v}",
	"guest.refused":        "asked for something",
}

// GuestPhrase says a guest's request plainly ({v}: the version).
func GuestPhrase(what string) string {
	if s, ok := guestPhrases[what]; ok {
		return s
	}
	return "asked for something"
}

func init() {
	pastOf["share"] = "shared"
	pastOf["comment"] = "commented"
	for k, v := range map[string]string{
		"guests.status": "check whether guests are let in", "guests.settings.set": "change whether guests are let in",
		"guests.share": "share {p} with a guest", "guests.list": "list the guests and what each may see",
		"guests.revoke": "take back what a guest was given", "guests.link": "make a sign-in link for a guest",
		"guests.activity": "read what guests did",
		"guest.shared":    "look at what was shared with them as a guest", "guest.watch": "open version {r} of {p} as a guest",
		"guest.comments": "read their comments on version {r} of {p} as a guest", "guest.comment": "comment on version {r} of {p} as a guest",
	} {
		opBase[k] = v
	}
	for k, v := range map[string]string{
		"guests.settings": "changed whether guests are let in", "guests.share": "shared {p} with a guest",
		"guests.link": "made a sign-in link for a guest", "guests.revoke": "took back what a guest was given",
	} {
		eventPhrases[k] = v
	}
	for k, v := range guestPhrases {
		eventPhrases[k] = strings.ReplaceAll(v, "{v}", "version {r} of {p}")
	}
}
