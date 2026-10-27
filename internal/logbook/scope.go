package logbook

import "context"

// Scope is what the current work belongs to: the operation entry (parent of
// the program runs and events it causes), the job a worker is running, and the
// objects involved. ops.Call and the worker put it in the context.
type Scope struct {
	EntryID string
	Op      string
	JobID   string
	Channel string
	Refs    Refs
	rec     *OpRecord
}

type scopeKey struct{}

// WithScope returns ctx carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeOf returns the scope in ctx (zero when there is none).
func ScopeOf(ctx context.Context) Scope {
	if ctx == nil {
		return Scope{}
	}
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}

// WithJob marks ctx as a worker running job jobID (program runs link to it).
func WithJob(ctx context.Context, jobID string, refs Refs) context.Context {
	refs.JobID = jobID
	return WithScope(ctx, Scope{JobID: jobID, Channel: ChannelWorker, Refs: refs})
}

// merge fills empty references of r from o.
func (r *Refs) merge(o Refs) {
	if r.ProjectID == "" {
		r.ProjectID = o.ProjectID
	}
	if r.AssetID == "" {
		r.AssetID = o.AssetID
	}
	if r.RevisionID == "" {
		r.RevisionID = o.RevisionID
	}
	if r.RenderID == "" {
		r.RenderID = o.RenderID
	}
	if r.JobID == "" {
		r.JobID = o.JobID
	}
	if r.QAReportID == "" {
		r.QAReportID = o.QAReportID
	}
	if r.TodoID == "" {
		r.TodoID = o.TodoID
	}
	if r.ConversationID == "" {
		r.ConversationID = o.ConversationID
	}
}

// refFromID files an ID under its reference by prefix (prj_ → project, …).
func (r *Refs) refFromID(id string) bool {
	i := 0
	for i < len(id) && i < 5 && id[i] != '_' {
		i++
	}
	if i >= len(id) || id[i] != '_' || len(id)-i-1 != 26 {
		return false
	}
	set := func(dst *string) bool {
		if *dst == "" {
			*dst = id
		}
		return true
	}
	switch id[:i] {
	case "prj":
		return set(&r.ProjectID)
	case "ast":
		return set(&r.AssetID)
	case "rev":
		return set(&r.RevisionID)
	case "rnd":
		return set(&r.RenderID)
	case "job":
		return set(&r.JobID)
	case "qar":
		return set(&r.QAReportID)
	case "tdo":
		return set(&r.TodoID)
	}
	return false
}
