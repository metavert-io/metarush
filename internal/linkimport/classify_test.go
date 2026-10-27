package linkimport

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in                           string
		platform, kind, videoID, url string
		list                         string
		err                          error
	}{
		{in: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", platform: "youtube", kind: KindVideo, videoID: "aqz-KE-bpKQ", url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ"},
		{in: "youtube.com/watch?v=aqz-KE-bpKQ&t=42s", platform: "youtube", kind: KindVideo, videoID: "aqz-KE-bpKQ", url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ"},
		{in: "https://m.youtube.com/watch?v=aqz-KE-bpKQ&list=PL6B3937A5D230E335", platform: "youtube", kind: KindVideo, videoID: "aqz-KE-bpKQ", url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", list: "PL6B3937A5D230E335"},
		{in: "https://youtu.be/aqz-KE-bpKQ?si=abc", platform: "youtube", kind: KindVideo, videoID: "aqz-KE-bpKQ", url: "https://www.youtube.com/watch?v=aqz-KE-bpKQ"},
		{in: "https://www.youtube.com/shorts/abcdefghijk", platform: "youtube", kind: KindVideo, videoID: "abcdefghijk", url: "https://www.youtube.com/watch?v=abcdefghijk"},
		{in: "https://www.youtube.com/live/abcdefghijk?feature=share", platform: "youtube", kind: KindVideo, videoID: "abcdefghijk"},
		{in: "https://www.youtube-nocookie.com/embed/abcdefghijk", platform: "youtube", kind: KindVideo, videoID: "abcdefghijk"},
		{in: "https://www.youtube.com/playlist?list=PL6B3937A5D230E335", platform: "youtube", kind: KindPlaylist, list: "PL6B3937A5D230E335", url: "https://www.youtube.com/playlist?list=PL6B3937A5D230E335"},
		{in: "https://www.youtube.com/@BlenderOfficial", platform: "youtube", kind: KindChannel, url: "https://www.youtube.com/@BlenderOfficial/videos"},
		{in: "https://www.youtube.com/@BlenderOfficial/shorts", platform: "youtube", kind: KindChannel, url: "https://www.youtube.com/@BlenderOfficial/shorts"},
		{in: "https://www.youtube.com/channel/UCSMOQeBJ2RAnuFungnQOxLg", platform: "youtube", kind: KindChannel},
		{in: "https://www.youtube.com/watch?v=tooShort", err: ErrNoVideo},
		{in: "https://www.youtube.com/results?search_query=cats", err: ErrNoVideo},
		{in: "https://www.tiktok.com/@gardenclub42/video/7000000000000000042?is_from_webapp=1", platform: "tiktok", kind: KindVideo, videoID: "7000000000000000042", url: "https://www.tiktok.com/@gardenclub42/video/7000000000000000042"},
		{in: "https://vm.tiktok.com/ZMabc123/", platform: "tiktok", kind: KindVideo, url: "https://vm.tiktok.com/ZMabc123"},
		{in: "https://www.tiktok.com/t/ZTRabc123/", platform: "tiktok", kind: KindVideo, url: "https://www.tiktok.com/t/ZTRabc123"},
		{in: "https://www.tiktok.com/@gardenclub42", platform: "tiktok", kind: KindChannel, url: "https://www.tiktok.com/@gardenclub42"},
		{in: "https://www.tiktok.com/@someone/photo/7300000000000000000", err: errSlideshow},
		{in: "https://x.com/primevideouk/status/1578401165338976258", platform: "x", kind: KindPost, videoID: "1578401165338976258", url: "https://x.com/primevideouk/status/1578401165338976258"},
		{in: "https://twitter.com/primevideouk/status/1578401165338976258?s=20", platform: "x", kind: KindPost, videoID: "1578401165338976258", url: "https://x.com/primevideouk/status/1578401165338976258"},
		{in: "https://mobile.twitter.com/primevideouk/status/1578401165338976258/video/2", platform: "x", kind: KindPost, videoID: "1578401165338976258", url: "https://x.com/primevideouk/status/1578401165338976258/video/2"},
		{in: "https://x.com/i/web/status/1578401165338976258", platform: "x", kind: KindPost, videoID: "1578401165338976258", url: "https://x.com/i/status/1578401165338976258"},
		{in: "https://x.com/primevideouk", platform: "x", kind: KindChannel, url: "https://x.com/primevideouk"},
		{in: "https://x.com/home", err: ErrNoVideo},
		{in: "https://vimeo.com/76979871#t=5", platform: "web", kind: KindVideo, url: "https://vimeo.com/76979871"},
		{in: "not a link", err: ErrNotALink},
		{in: "javascript:alert(1)", err: ErrNotALink},
		{in: "file:///etc/passwd", err: ErrNotALink},
		{in: "ftp://example.com/video.mp4", err: ErrNotALink},
		{in: "https://user:pass@example.com/video", err: ErrNotALink},
		{in: "http://127.0.0.1:4845/api/ops/asset.list", err: ErrLocalLink},
		{in: "http://localhost/clip.mp4", err: ErrLocalLink},
		{in: "http://192.168.1.20/clip.mp4", err: ErrLocalLink},
		{in: "http://[::1]/clip.mp4", err: ErrLocalLink},
		{in: "http://nas/clip.mp4", err: ErrLocalLink},
		{in: "http://my-mac.local/clip.mp4", err: ErrLocalLink},
	}
	for _, c := range cases {
		l, err := Classify(c.in)
		switch {
		case c.err == errSlideshow:
			if err == nil {
				t.Errorf("%s: want a slideshow refusal", c.in)
			}
			continue
		case c.err != nil:
			if !errors.Is(err, c.err) {
				t.Errorf("%s: err %v, want %v", c.in, err, c.err)
			}
			continue
		case err != nil:
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if l.Platform != c.platform || l.Kind != c.kind || l.VideoID != c.videoID {
			t.Errorf("%s: got %s/%s/%q, want %s/%s/%q", c.in, l.Platform, l.Kind, l.VideoID, c.platform, c.kind, c.videoID)
		}
		if c.url != "" && l.URL != c.url {
			t.Errorf("%s: url %s, want %s", c.in, l.URL, c.url)
		}
		if l.ListID != c.list {
			t.Errorf("%s: list %q, want %q", c.in, l.ListID, c.list)
		}
		if l.Many() != (c.kind == KindPlaylist || c.kind == KindChannel) {
			t.Errorf("%s: Many() = %v", c.in, l.Many())
		}
	}
}

var errSlideshow = errors.New("slideshow")

func TestPlaylistVideoSaysOnlyTheVideo(t *testing.T) {
	l, err := Classify("https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=PL6B3937A5D230E335&index=3")
	if err != nil || l.Note == "" || l.Many() {
		t.Fatalf("a video opened from a playlist is one video with a note: %+v %v", l, err)
	}
}

func TestFindLinks(t *testing.T) {
	got := FindLinks("look: https://youtu.be/aqz-KE-bpKQ and <https://x.com/a/status/12345678901>\n# comment\nhttps://youtu.be/aqz-KE-bpKQ ftp://x")
	if len(got) != 2 || got[0] != "https://youtu.be/aqz-KE-bpKQ" || got[1] != "https://x.com/a/status/12345678901" {
		t.Fatalf("FindLinks: %v", got)
	}
}

func TestIdempotencyKey(t *testing.T) {
	a, _ := Classify("https://youtu.be/aqz-KE-bpKQ")
	b, _ := Classify("https://www.youtube.com/watch?v=aqz-KE-bpKQ&t=3")
	if IdempotencyKey("ins_1", a, "prj_1") != IdempotencyKey("ins_1", b, "prj_1") {
		t.Fatal("the same video pasted two ways must join the same import")
	}
	if IdempotencyKey("ins_1", a, "prj_1") == IdempotencyKey("ins_1", a, "prj_2") || IdempotencyKey("ins_1", a, "") == IdempotencyKey("ins_2", a, "") {
		t.Fatal("another project or machine is another import")
	}
	p, _ := Classify("https://x.com/a/status/1578401165338976258/video/2")
	q, _ := Classify("https://x.com/a/status/1578401165338976258")
	if IdempotencyKey("ins_1", p, "") == IdempotencyKey("ins_1", q, "") {
		t.Fatal("one video of a post is not the whole post")
	}
}
