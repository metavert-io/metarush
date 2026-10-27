package cli

import (
	"testing"

	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// A linked folder's files that couldn't be added are listed under it, each
// with why, as the dashboard shows them; its line says when they are tried again.
func TestLinkedFolderListsWhatCouldntBeAdded(t *testing.T) {
	f := intake.LinkedFolderRow{Name: "Logos", ProjectName: "Garden", HerePath: "~/Logos",
		Text: "Kept in step: 0 resources from 1 file. 1 file couldn't be added; MetaRush tries it again in about a minute.",
		Here: &models.LinkedFolderMachine{Failed: 1, Errors: []string{"mark.png: That project was not found. It may have been deleted."}}}
	got := stdout(t, func() { printLinkedFolder(f, false) })
	want := "Logos → Garden: Kept in step: 0 resources from 1 file. 1 file couldn't be added; MetaRush tries it again in about a minute.\n" +
		"  here: ~/Logos\n" +
		"  couldn't add mark.png: That project was not found. It may have been deleted.\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if fs := plainwords.Check(f.Text + "\n  couldn't add " + f.Here.Errors[0]); len(fs) > 0 {
		t.Fatalf("not plain: %v", fs)
	}
}
