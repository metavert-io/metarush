package api

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
)

// The ops endpoint takes 8 MB requests, and more for an op that carries a
// file: a 9 MB picture attached from the dashboard goes through (it used to
// stop at about 6 MB), and a request over an op's limit is refused plainly.
func TestOpsTakeLargerRequestsWhereAnOpCarriesAFile(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	d.signIn()
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 9<<20)...)
	var out struct {
		File struct {
			Size int64  `json:"size"`
			MIME string `json:"mime"`
		} `json:"file"`
	}
	d.op("assistant.attach", map[string]any{"name": "board.png", "data": base64.StdEncoding.EncodeToString(png)}, &out)
	if out.File.Size != int64(len(png)) || out.File.MIME != "image/png" {
		t.Fatalf("attached %+v", out.File)
	}
	big := map[string]any{"query": strings.Repeat("x", 9<<20)}
	code, env := d.do("POST", "/api/ops/search", big, true)
	if code != http.StatusBadRequest || env.Error.Code != "E_INVALID_INPUT" || !strings.Contains(env.Error.Message, "too large") {
		t.Fatalf("an oversized request: %d %+v", code, env.Error)
	}
}
