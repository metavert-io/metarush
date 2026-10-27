package api

import (
	"mime"
	"testing"
)

// Download names every computer keeps (D247): Windows' refused characters
// become "-", device names get "_", a trailing dot or space goes, the
// extension stays; non-ASCII names travel as UTF-8 and come back whole.
func TestDownloadNames(t *testing.T) {
	for in, want := range map[string]string{
		"clip.mov":                        "clip.mov",
		`Q&A: what's next?.mov`:           "Q&A- what's next-.mov",
		`C:\Users\Ana\Videos\harbour.mp4`: "C--Users-Ana-Videos-harbour.mp4",
		"take 2 <final> | cut*.mp4":       "take 2 -final- - cut-.mp4",
		"CON.mp4":                         "_CON.mp4",
		"nul":                             "_nul",
		"com3.tar.gz":                     "_com3.tar.gz",
		"trailing dot. .mov":              "trailing dot.mov",
		"notes. ":                         "notes",
		"tab\there.wav":                   "tabhere.wav",
		"":                                "download",
		"...":                             "download",
		"Café — Ana Müller (版本 2).mov":    "Café — Ana Müller (版本 2).mov",
		"metarush-r3-vertical-abc123.mp4": "metarush-r3-vertical-abc123.mp4",
	} {
		if got := downloadName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	long := ""
	for i := 0; i < 300; i++ {
		long += "é"
	}
	if got := []rune(downloadName(long + ".mov")); len(got) != 184 || string(got[180:]) != ".mov" {
		t.Errorf("a long name is cut before its extension at a whole character: %d", len(got))
	}
	if h := attachment("Harbour.mp4"); h != `attachment; filename="Harbour.mp4"` {
		t.Errorf("an ASCII name: %q", h)
	}
	if h := attachment("Café.mov"); h != `attachment; filename="Caf_.mov"; filename*=UTF-8''Caf%C3%A9.mov` {
		t.Errorf("a name with an accent: %q", h)
	}
	for _, name := range []string{"clip.mov", "Café — Ana Müller (版本 2).mov", `a "quoted" name.mp4`} {
		h := attachment(name)
		disp, params, err := mime.ParseMediaType(h)
		if err != nil || disp != "attachment" || params["filename"] != downloadName(name) {
			t.Errorf("%q: header %q reads back as %q %v (%v)", name, h, disp, params, err)
		}
	}
}
