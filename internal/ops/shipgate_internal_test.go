package ops

import (
	"context"
	"os"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A failed note that a version went out anyway is an error the callers say
// (never silently dropped); the home folder and the top of a disk or volume
// are never a client's delivery folder.
func TestSentOutAndFolderRules(t *testing.T) {
	a := testapp.New(t)
	err := recordSentOut(context.Background(), a, "rev_00000000000000000000000000", []models.ShipOverride{{ID: "sho_x"}}, "delivery", "Acme")
	if err == nil {
		t.Fatal("recording on a version that isn't there said nothing")
	}
	home, _ := os.UserHomeDir()
	for _, d := range []string{home, "/", "/Volumes/Client drive", "/media/usb", "/mnt/share"} {
		if !notClientFolder(d) {
			t.Errorf("%s may be a client's delivery folder", d)
		}
	}
	if notClientFolder(t.TempDir()) {
		t.Error("an ordinary folder is never a client's")
	}
}
