package wsarchive

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// transformFunc changes one document as it goes into an archive. at is when
// the archive was made. It reports whether it changed anything.
type transformFunc func(doc bson.D, at time.Time) (bson.D, bool)

// clearPasswordSet: passwords are not in an archive (user_credentials is left
// out), so a person's "password set" would be untrue in the copy.
func clearPasswordSet(doc bson.D, _ time.Time) (bson.D, bool) {
	return unset(doc, "passwordSetAt")
}

// dropUploadSession takes out an unfinished upload's resumable address: a
// Google Cloud Storage session URI authorizes writing that object for a week
// without any other key.
func dropUploadSession(doc bson.D, _ time.Time) (bson.D, bool) {
	up, ok := get(doc, "upload").(bson.D)
	if !ok {
		return doc, false
	}
	up, changed := unset(up, "sessionUri")
	if !changed {
		return doc, false
	}
	return set(doc, "upload", up), true
}

// HeldMessage is what work held in a copy says (D330).
const HeldMessage = "Held for a person: it was running, or it costs money or acts outside the workspace, when the workspace was exported. Start it again here once you're sure the original isn't doing it."

// paidWork are kinds of work that spend money or act outside the workspace
// (AI services, the AI voice, abilities, Claude Code's scene descriptions,
// pushes to repositories, a source's own steps, downloads, a Remotion project
// brought in from GitHub with the person's sign-in): never run by a copy by
// itself, beside an original that may be running them too. Every kind pinned
// to the computer that asked is one of these but two that only work inside
// the workspace's own storage: a linked folder's file uploaded (it waits for
// its computer) and a storage move (only the process that asked runs it).
var paidWork = map[string]bool{"generate": true, "voiceover": true, "ability-run": true, "describe": true,
	"remotion-refresh": true, "remotion-push": true, "remotion-import": true, "fetch": true, "import": true}

// holdJob holds work for a person in a copy: what was running (the computer
// running it isn't in the archive, and the original may still finish it) and
// paid work that was waiting. It is stopped the way a person stops work —
// cancelled, with the attempt that was running not counted and marked
// released — so Retry (job.retry) starts it again here; nothing takes it by
// itself. Other waiting work waits as before.
func holdJob(doc bson.D, at time.Time) (bson.D, bool) {
	state, _ := get(doc, "state").(string)
	typ, _ := get(doc, "type").(string)
	running := state == "running"
	if !running && !(state == "queued" && paidWork[typ]) {
		return doc, false
	}
	doc = set(doc, "state", "canceled")
	doc = set(doc, "cancelRequested", false)
	doc, _ = unset(doc, "lease")
	if running {
		switch n := get(doc, "attemptCount").(type) {
		case int32:
			if n > 0 {
				doc = set(doc, "attemptCount", n-1)
			}
		case int64:
			if n > 0 {
				doc = set(doc, "attemptCount", n-1)
			}
		}
		if atts, ok := get(doc, "attempts").(bson.A); ok {
			for i := len(atts) - 1; i >= 0; i-- {
				a, ok := atts[i].(bson.D)
				if !ok {
					continue
				}
				if get(a, "endedAt") == nil {
					a = set(a, "endedAt", bson.NewDateTimeFromTime(at))
					a = set(a, "outcome", "released")
					atts[i] = a
				}
				break
			}
			doc = set(doc, "attempts", atts)
		}
	}
	doc = set(doc, "progress", bson.D{{Key: "message", Value: HeldMessage}})
	doc = set(doc, "error", HeldMessage)
	doc = set(doc, "held", bson.D{{Key: "at", Value: bson.NewDateTimeFromTime(at)}, {Key: "was", Value: state}})
	doc = set(doc, "finishedAt", bson.NewDateTimeFromTime(at))
	doc = set(doc, "updatedAt", bson.NewDateTimeFromTime(at))
	return doc, true
}

// holdRender says a render that was being made is held with its work (Retry
// of the job queues it again).
func holdRender(doc bson.D, at time.Time) (bson.D, bool) {
	if s, _ := get(doc, "state").(string); s != "rendering" && s != "uploading" {
		return doc, false
	}
	doc = set(doc, "state", "canceled")
	doc = set(doc, "error", HeldMessage)
	doc = set(doc, "held", bson.D{{Key: "at", Value: bson.NewDateTimeFromTime(at)}})
	return doc, true
}

// idleConversation marks a Claude Code conversation that was answering as
// waiting: the Claude Code process answering it was on a computer, not in
// the archive.
func idleConversation(doc bson.D, _ time.Time) (bson.D, bool) {
	changed := false
	if s, _ := get(doc, "state").(string); s == "working" {
		doc, changed = set(doc, "state", "idle"), true
	}
	switch p := get(doc, "holderPid").(type) {
	case int32:
		if p != 0 {
			doc, changed = set(doc, "holderPid", int32(0)), true
		}
	case int64:
		if p != 0 {
			doc, changed = set(doc, "holderPid", int64(0)), true
		}
	}
	return doc, changed
}

// get is a top-level field's value (nil when it isn't there).
func get(d bson.D, key string) any {
	for _, e := range d {
		if e.Key == key {
			return e.Value
		}
	}
	return nil
}

// set replaces a top-level field in place, or adds it at the end.
func set(d bson.D, key string, v any) bson.D {
	for i, e := range d {
		if e.Key == key {
			d[i].Value = v
			return d
		}
	}
	return append(d, bson.E{Key: key, Value: v})
}

// unset removes a top-level field, keeping the others' order.
func unset(d bson.D, key string) (bson.D, bool) {
	for i, e := range d {
		if e.Key == key {
			return append(d[:i:i], d[i+1:]...), true
		}
	}
	return d, false
}
