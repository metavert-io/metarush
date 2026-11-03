package recipes

import (
	"os"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/testutil/fakeytdlp"
)

func TestMain(m *testing.M) {
	fakerclone.MaybeRun() // the stand-in rclone for Google Drive folders (D220)
	fakeytdlp.MaybeRun()  // the stand-in yt-dlp for links to start from (D401)
	os.Exit(m.Run())
}
