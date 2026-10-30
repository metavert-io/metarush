package models

import "strings"

// NeverShown are the stored fields MetaRush keeps but never shows or sends to
// a browser — `json:"-"` on its records: capabilities (a review link's key,
// a storage key, a resumable upload's address), sign-in state, voice and face
// prints, claims and leases on work, and what conversations keep for Claude
// alone. Where a record is shown whole — something written into an old
// database after a move, beside this database's own (D359) — these are left
// out wherever they appear in it, by name. TestNeverShownIsWhole keeps the
// list in step with every record's `json:"-"` fields; a few names that hold
// secrets on their own are listed too, and any field whose name looks like a
// secret is left out as well (LooksSecret), so a record's unknown or free-form
// fields are covered.
var NeverShown = map[string]bool{
	// Capabilities and where files are kept.
	"key": true, "storageKey": true, "sessionUri": true, "store": true,
	// Sign-in state.
	"emailKey": true, "sessionsValidAfter": true, "sessions": true, "sessionTokens": true, "sessionStarted": true,
	"hash": true, "tokenHash": true, "secret": true, "password": true, "refreshToken": true, "accessToken": true,
	// A review link's readers and its own bookkeeping.
	"writer": true, "seen": true, "taken": true, "owners": true, "seatSize": true, "hour": true, "inHour": true,
	"syncUntil": true, "syncBy": true, "lastRead": true,
	// Voice and face prints.
	"v": true, "prints": true, "printModel": true, "centroid": true, "exemplars": true, "exSeconds": true,
	"nameKey": true, "notNames": true, "voices": true, "before": true, "speakerFrom": true,
	// Claims and leases on work, and who holds what.
	"claim": true, "lock": true, "lease": true, "holderPid": true, "pending": true, "pendingSince": true,
	"installation": true, "run": true, "work": true,
	// What a conversation keeps for Claude alone, and how it was told.
	"inbox": true, "news": true, "told": true, "forClaude": true, "question": true, "input": true, "batch": true,
	"counting": true, "folder": true, "refPrefix": true, "renderDoneAt": true, "handover": true, "to": true, "from": true,
	// Other records' own state.
	"log": true, "schemaJson": true, "previewJson": true, "oldDir": true, "newDir": true,
}

// secretWords are what a field's name says when it holds a secret, in lower
// case without separators.
var secretWords = []string{"token", "secret", "password", "passwd", "passphrase", "apikey", "credential", "privatekey", "signingkey", "accesskey", "cookie", "authorization", "bearer", "sessionuri", "refreshkey"}

// LooksSecret reports a field whose name looks like it holds a secret: an
// apiToken, a clientSecret, a password_hash.
func LooksSecret(name string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(name))
	for _, w := range secretWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// HiddenField reports a field a record shown whole leaves out: one on
// NeverShown, or one whose name looks like a secret and that holds something
// a secret can be in (text: tokenLimitMillions, a number, says nothing).
func HiddenField(name string, holdsText bool) bool {
	return NeverShown[name] || (holdsText && LooksSecret(name))
}
