package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/auth"
)

// A person's picture (D184): any PNG, JPEG or WebP becomes a 256-pixel
// square; something else is refused; only signed-in browsers see it.
func TestAvatars(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn()
	img := image.NewRGBA(image.Rect(0, 0, 400, 240)) // not square: the middle is kept
	for x := 0; x < 400; x++ {
		for y := 0; y < 240; y++ {
			img.Set(x, y, color.RGBA{uint8(x / 2), 90, 160, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	var out auth.AvatarOut
	d.op("account.avatar", map[string]any{"data": base64.StdEncoding.EncodeToString(buf.Bytes())}, &out)
	if !strings.HasPrefix(out.Account.Avatar, "/media/avatars/"+d.a.Cfg.User.ID+"?v=") {
		t.Fatalf("avatar: %+v", out)
	}
	var list auth.UsersListOut
	d.op("users.list", map[string]any{}, &list)
	if list.People[0].Avatar == "" {
		t.Fatalf("the people list shows it: %+v", list.People)
	}
	var sess Session
	_, env := d.do("GET", "/api/session", nil, false)
	_ = json.Unmarshal(env.Data, &sess)
	if sess.Avatar != out.Account.Avatar {
		t.Fatalf("the session carries it: %+v", sess)
	}
	req, _ := http.NewRequest("GET", d.base+out.Account.Avatar, nil)
	res, err := d.client.Do(req)
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("served: %v %+v", err, res)
	}
	got, err := png.Decode(res.Body)
	res.Body.Close()
	if err != nil || got.Bounds().Dx() != 256 || got.Bounds().Dy() != 256 {
		t.Fatalf("a 256-pixel square: %v %v", err, got.Bounds())
	}
	if res, _, _ := d.tailnet("", "").do("GET", out.Account.Avatar, nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("a signed-out browser saw it: %d", res.StatusCode)
	}
	if code, env := d.do("POST", "/api/ops/account.avatar", map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)>"))}, true); code == 200 || !strings.Contains(env.Error.Message, "isn't a picture") {
		t.Fatalf("not a picture: %d %+v", code, env.Error)
	}
	var removed auth.AvatarOut
	d.op("account.avatar", map[string]any{"remove": true}, &removed)
	if removed.Account.Avatar != "" || removed.Account.ID == "" {
		t.Fatalf("removed: %+v", removed.Account)
	}
}
