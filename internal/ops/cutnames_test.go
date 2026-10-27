package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Cuts and versions get descriptive names (D150): a version's title names the
// cut only when it says what the cut is ("fernbrook main v2" doesn't); otherwise
// MetaRush suggests one from the brief ("Main awareness video") or the cut's
// shape; a version's note is the change's own, else what was asked.
func TestCutNamesAndVersionNotes(t *testing.T) {
	for _, c := range []struct {
		title string
		want  bool
	}{{"fernbrook main v2", false}, {"Cut", false}, {"Final v3 copy", false}, {"Spring open house teaser", true}, {"Main awareness video", true}} {
		if got := DescriptiveTitle("fernbrook", "main", c.title); got != c.want {
			t.Errorf("DescriptiveTitle(%q) = %v", c.title, got)
		}
	}
	p := &models.Project{Name: "fernbrook", Brief: models.Brief{Goal: "Raise awareness of the spring open house"}, Branches: map[string]models.BranchHead{"main": {}}}
	if got := SuggestCutName(p, "main", nil); got != "Awareness video" {
		t.Errorf("one cut: %q", got)
	}
	p.Branches["shorts"] = models.BranchHead{}
	if got := SuggestCutName(p, "main", nil); got != "Main awareness video" {
		t.Errorf("main of two: %q", got)
	}
	if got := SuggestCutName(p, "shorts", nil); got != "Shorts awareness video" {
		t.Errorf("another cut: %q", got)
	}
	plain := &models.Project{Name: "fernbrook", Branches: map[string]models.BranchHead{"main": {}}}
	head := &models.Revision{Duration: 44.6, Spec: editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical}}}
	if got := SuggestCutName(plain, "main", head); got != "45-second vertical cut" {
		t.Errorf("shape and length: %q", got)
	}
	if n, by := versionNote(&editspec.Plan{Note: "flicker fixes and headline cards", Instruction: "x"}, nil, models.ViaAssistant); n != "flicker fixes and headline cards" || by != models.NoteByClaude {
		t.Errorf("Claude's note: %q %q", n, by)
	}
	if n, by := versionNote(&editspec.Plan{Instruction: "Tighten the middle. Then add music."}, nil, "cli"); n != "tighten the middle" || by != models.NoteByMetaRush {
		t.Errorf("from the request: %q %q", n, by)
	}
	if n, _ := versionNote(&editspec.Plan{Instruction: "TV spot for Lena"}, nil, "cli"); n != "TV spot for Lena" {
		t.Errorf("capitals kept: %q", n)
	}
	if n, _ := versionNote(&editspec.Plan{Instruction: "“Kitchen chat” plays its fixed copy"}, nil, "cli"); n != "“Kitchen chat” plays its fixed copy" {
		t.Errorf("quoted words first: %q", n)
	}
	if n, _ := versionNote(&editspec.Plan{Instruction: "Éclairer la fin"}, nil, "cli"); n != "éclairer la fin" {
		t.Errorf("a capital that isn't ASCII: %q", n)
	}

	// In a project: the suggestion until someone names the cut; the name
	// survives new versions; a version's note changes without a new version.
	a := testapp.New(t)
	ctx := context.Background()
	pr := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "fernbrook", Brief: models.Brief{Goal: "an awareness video for the open house"}})
	as := storedAsset(t, a, pr.Project.ID, "walk.mov", "video", models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30})
	base := ""
	save := func(title, instruction string) *EditApplyOut {
		t.Helper()
		baseField := ""
		if base != "" {
			baseField = fmt.Sprintf(`"baseRevisionId":%q,`, base)
		}
		plan := fmt.Sprintf(`{"projectId":%q,%s"instruction":%q,"spec":{"schemaVersion":1,"title":%q,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":1,"out":5}]}}`,
			pr.Project.ID, baseField, instruction, title, as.ID)
		chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
		out := call[EditApplyOut](t, a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken})
		base = out.Revision.ID
		return out
	}
	v1 := save("fernbrook main v2", "First cut of the walk")
	show := call[ProjectShowOut](t, a, "project.show", ProjectShowIn{ID: pr.Project.ID})
	if len(show.Cuts) != 1 || show.Cuts[0].Name != "Awareness video" || show.Cuts[0].NameBy != "suggested" {
		t.Fatalf("cuts: %+v", show.Cuts)
	}
	if v1.Revision.Note != "first cut of the walk" {
		t.Fatalf("note: %q", v1.Revision.Note)
	}
	named := call[CutView](t, a, "cut.rename", CutRenameIn{ProjectID: pr.Project.ID, Suggested: true})
	if named.Name != "Awareness video" || named.NameBy != models.TitleByMetaRush {
		t.Fatalf("took the suggestion: %+v", named)
	}
	named = call[CutView](t, a, "cut.rename", CutRenameIn{ProjectID: pr.Project.ID, Title: "Open house walk"})
	if named.Name != "Open house walk" || named.NameBy != models.TitleByPerson {
		t.Fatalf("named: %+v", named)
	}
	v2 := save("fernbrook main v3", "Trim the start") // the same clip, a new title: a new version
	if got := CutName(ctx, a, mustProject(t, a, pr.Project.ID), "main"); got != "Open house walk" || v2.Revision.Number != 2 {
		t.Fatalf("the name survives a new version: %q", got)
	}
	n := call[RevisionSummary](t, a, "revision.note", RevisionNoteIn{ID: v1.Revision.ID, Note: "the walk, first try"})
	if n.Note != "the walk, first try" || n.NoteBy != models.NoteByPerson {
		t.Fatalf("note: %+v", n)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), v1.Revision.ID, &r)
	if r.SpecHash != v1.Revision.SpecHash || r.Note != "the walk, first try" {
		t.Fatal("only the note changes")
	}
	if n := call[RevisionSummary](t, a, "revision.note", RevisionNoteIn{ID: v1.Revision.ID}); n.Note != "" {
		t.Fatalf("removed: %+v", n)
	}
}

// mustProject is shared with the sign-off tests (signoffs_test.go).
