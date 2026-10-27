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

// runningJobMessage is what a job put back as waiting says.
const runningJobMessage = "waiting (it was running when the workspace was exported)"

// releaseRunningJob puts work that was running back as waiting, the way a
// worker that stops releases its job: the lease taken off, the attempt not
// counted and marked released (the computer running it isn't in the
// archive). Work pinned to one computer stays pinned to it.
func releaseRunningJob(doc bson.D, at time.Time) (bson.D, bool) {
	if s, _ := get(doc, "state").(string); s != "running" {
		return doc, false
	}
	doc = set(doc, "state", "queued")
	doc, _ = unset(doc, "lease")
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
	doc = set(doc, "progress", bson.D{{Key: "message", Value: runningJobMessage}})
	doc = set(doc, "updatedAt", bson.NewDateTimeFromTime(at))
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
