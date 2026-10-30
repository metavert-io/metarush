package models

// NeverShown are the stored fields MetaRush keeps but never shows or sends to
// a browser — `json:"-"` on its records: capabilities (a review link's key,
// a storage key, a resumable upload's address), sign-in state, voice and face
// prints, claims and leases on work, and what conversations keep for Claude
// alone. Where a record is shown whole — something written into an old
// database after a move, beside this database's own (D359) — these are left
// out wherever they appear in it, by name. TestNeverShownIsWhole keeps the
// list in step with every record's `json:"-"` fields; a few names that hold
// secrets on their own are listed too.
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
