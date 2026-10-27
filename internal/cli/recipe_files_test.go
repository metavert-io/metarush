package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The commands of D219 and D220 say plainly what they need: a recipe file that
// is there, --from-drive as input=remote:path, a kept motion graphic's whole
// version.
func TestRecipeFileAndDriveCommandsSayWhatTheyNeed(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	code := func(r cliRun) string {
		t.Helper()
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.stdout), &env)
		return env.Error.Code + ": " + env.Error.Message
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"recipe", "import", filepath.Join(t.TempDir(), "none.recipe.zip"), "--json"}, apperr.NotFound + ": There's no recipe file there."},
		{[]string{"recipe", "export", "nope", "--json"}, apperr.NotFound + ": There's no recipe called “nope”."},
		{[]string{"recipe", "update", "nope", "--from-drive", "art", "--json"}, apperr.InvalidInput + ": Give --from-drive as input=remote:path"},
		{[]string{"motion", "check", "--json"}, apperr.InvalidInput + ": Say which motion graphic to check"},
		{[]string{"motion", "check", "--version", "lower-bar@12345678", "--json"}, apperr.InvalidInput + ": Say which kept version to check"},
	} {
		r := runCLI(t, home, c.args...)
		if got := code(r); r.code == 0 || !strings.HasPrefix(got, c.want) {
			t.Errorf("%v: %d %q (stderr %q)", c.args, r.code, got, r.stderr)
		}
	}
}
