package logbook

import "testing"

// The Library's actions read in plain words, and the warning before deleting
// for good (a dry run) never reads as if something was deleted.
func TestLibraryActionsReadInPlainWords(t *testing.T) {
	names := &Names{Assets: map[string]string{"ast_01jabcdefghjkmnpqrstvwxyz0": "Kitchen chat"}}
	entry := func(op string, input map[string]any) *Entry {
		return &Entry{Kind: KindOp, Refs: Refs{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz0"}, Op: &OpInfo{Name: op, Mutating: true, OK: true, Input: input}}
	}
	for _, c := range []struct {
		op   string
		in   map[string]any
		want string
	}{
		{"asset.archive", nil, "Archived clips"},
		{"asset.trash", nil, "Moved clips to the Trash"},
		{"asset.restore", nil, "Restored clips from the Trash"},
		{"collection.add", nil, "Added clips to a collection"},
		{"trash.empty", nil, "Emptied the Trash"},
		{"trash.empty", map[string]any{"dryRun": true}, "Checked what emptying the Trash would delete"},
		{"asset.delete", map[string]any{"dryRun": true}, "Checked whether “Kitchen chat” can be deleted"},
		{"asset.delete", map[string]any{"forever": true}, "Deleted “Kitchen chat” permanently"},
		{"trash.configure", nil, "Changed when the Trash empties itself"},
	} {
		if got := Describe(entry(c.op, c.in), names, ""); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.op, c.in, got, c.want)
		}
	}
}
