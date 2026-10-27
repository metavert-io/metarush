package recipes

import (
	"os"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
)

func TestMain(m *testing.M) {
	fakerclone.MaybeRun() // the stand-in rclone for Google Drive folders (D220)
	os.Exit(m.Run())
}
