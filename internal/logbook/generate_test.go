package logbook

import "testing"

// A generated shot's job reads as what was asked for and from whom, never as
// "the generated video for a clip" (the project is named, the prompt never is).
func TestGenerateJobsReadInPlainWords(t *testing.T) {
	names := &Names{Projects: map[string]string{"prj_01jabcdefghjkmnpqrstvwxyz0": "Pond film"}}
	job := func(event string, detail map[string]any) *Entry {
		return &Entry{Kind: KindJob, Refs: Refs{ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0"}, Job: &JobInfo{Type: "generate", Event: event, Detail: detail}}
	}
	shot := map[string]any{"what": "a 3-second shot", "provider": "Google Gemini", "take": 1}
	for _, c := range []struct {
		e    *Entry
		want string
	}{
		{job(JobLeased, shot), "Started making a 3-second shot with Google Gemini for Pond film"},
		{job(JobSucceeded, shot), "Made a 3-second shot with Google Gemini for Pond film"},
		{job(JobFailed, shot), "Couldn't make a 3-second shot with Google Gemini for Pond film"},
		{job(JobLeased, map[string]any{"what": "a 6-second shot", "provider": "Google Gemini", "take": 3.0}), "Started making a 6-second shot (take 3) with Google Gemini for Pond film"},
		{&Entry{Kind: KindJob, Job: &JobInfo{Type: "generate", Event: JobQueued, Detail: map[string]any{"what": "a test shot of a look", "provider": "Google Gemini"}}},
			"Waiting to start: making a test shot of a look with Google Gemini"},
	} {
		if got := Describe(c.e, names, ""); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
