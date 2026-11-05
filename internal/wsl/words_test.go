package wsl

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

func idle(ms int64) *int64 { return &ms }

// What MetaRush says inside WSL: WSL 1 refused with how to move on, the
// background service by systemd's state, Ubuntu staying up by Windows'
// setting, the browser by interop, the network, and folders on the Windows
// side — all in plain words, the commands kept apart.
func TestReport(t *testing.T) {
	if ReportFor(Info{}, WindowsSettings{}, nil) != nil {
		t.Fatal("no report outside WSL")
	}
	one := ReportFor(Info{Version: 1, Distro: "Ubuntu-20.04", DistroKnown: true}, WindowsSettings{}, nil)
	if len(one.Notes) != 1 || one.Notes[0].Key != "wsl-version" || one.Notes[0].Ready || !strings.Contains(one.Notes[0].Command, "wsl --set-version Ubuntu-20.04 2") ||
		!strings.Contains(one.Says, "WSL 1") || one.Ready() {
		t.Fatalf("WSL 1: %+v", one)
	}

	i := Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, Systemd: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: map[string]string{"/mnt/c": "c"}}
	r := ReportFor(i, WindowsSettings{Found: true, IdleTimeoutMS: idle(-1)}, nil)
	if r.Says != "MetaRush runs in Ubuntu on Windows (WSL 2)." || !r.Ready() {
		t.Fatalf("all set: %+v", r)
	}
	for _, k := range []string{"background", "keep-running", "browser"} {
		if n, ok := r.Note(k); !ok || !n.Ready {
			t.Fatalf("%s: %+v", k, n)
		}
	}
	if _, ok := r.Note("network"); ok {
		t.Fatal("nothing to say about NAT")
	}

	i.Systemd, i.SystemdAsked, i.Interop = false, false, false
	r = ReportFor(i, WindowsSettings{}, []Place{{What: PlaceHome, Path: "/home/you/.metarush"}, {What: PlaceStorage, Path: "/mnt/c/MetaRush/media"},
		{What: PlaceWatched, Path: "/mnt/c/Users/Sam/Videos/Inbox"}, {What: PlaceLinked, Path: "/home/you/Logos"}})
	bg, _ := r.Note("background")
	if bg.Ready || !strings.Contains(bg.Says, "without systemd") || !strings.Contains(bg.Command, "[boot]\nsystemd=true") || !strings.Contains(bg.Command, "wsl --shutdown") {
		t.Fatalf("no systemd: %+v", bg)
	}
	keep, _ := r.Note("keep-running")
	if keep.Ready || !strings.Contains(keep.Says, "about 15 seconds") || !strings.Contains(keep.Command, "instanceIdleTimeout=-1") {
		t.Fatalf("keep running: %+v", keep)
	}
	br, _ := r.Note("browser")
	if br.Ready || !strings.Contains(br.Next, "Edge or Chrome") {
		t.Fatalf("no interop: %+v", br)
	}
	var files []Note
	for _, n := range r.Notes {
		if n.Key == "files" {
			files = append(files, n)
		}
	}
	if len(files) != 2 || !strings.Contains(files[0].Says, "footage and finished videos are on the Windows side") && !strings.Contains(files[0].Says, "on the Windows side (C:)") ||
		!strings.Contains(files[1].Says, "watched folder") || !strings.Contains(files[0].Next, "Settings → Storage") {
		t.Fatalf("folders on the Windows side: %+v", files)
	}

	i.SystemdAsked = true
	if n := i.Background(); n.Ready || !strings.Contains(n.Says, "hasn't restarted") || !strings.Contains(n.Command, "wsl --shutdown") {
		t.Fatalf("asked, not restarted: %+v", n)
	}
	if n := i.KeepRunning(WindowsSettings{IdleTimeoutMS: idle(120000)}); n.Ready || !strings.Contains(n.Says, "about 2 minutes") {
		t.Fatalf("two minutes: %+v", n)
	}
	if n, ok := i.Network(WindowsSettings{Networking: "mirrored"}); !ok || !n.Ready || !strings.Contains(n.Says, "listens only on this PC") {
		t.Fatalf("mirrored: %+v", n)
	}
	if n, ok := i.Network(WindowsSettings{LocalhostOff: true}); !ok || n.Ready || !strings.Contains(n.Command, "localhostForwarding=true") {
		t.Fatalf("localhost forwarding off: %+v", n)
	}
	if n, ok := i.Network(WindowsSettings{Networking: "mirrored", LocalhostOff: true}); !ok || !n.Ready {
		t.Fatalf("mirrored ignores localhost forwarding: %+v", n)
	}
	if SlowFiles(1) == SlowFiles(2) || !strings.Contains(SlowFiles(3), "3 of these files") {
		t.Fatal("slow files")
	}
}

// Every sentence people read passes the plain-words check (commands, files
// and settings' names stay in Command).
func TestWordsArePlain(t *testing.T) {
	var says []string
	add := func(n Note) { says = append(says, n.Says, n.Next) }
	for _, i := range []Info{
		{Version: 1, Distro: "Ubuntu"},
		{Version: 2, Distro: "Ubuntu", Interop: true, Systemd: true},
		{Version: 2, Distro: "Debian", SystemdAsked: true},
		{Version: 2},
	} {
		says = append(says, i.Headline())
		if i.Version == 1 {
			add(i.WSL1())
			continue
		}
		add(i.Background())
		add(i.KeepRunning(WindowsSettings{}))
		add(i.KeepRunning(WindowsSettings{IdleTimeoutMS: idle(-1)}))
		add(i.Browser())
		for _, ws := range []WindowsSettings{{Networking: "mirrored"}, {LocalhostOff: true}} {
			if n, ok := i.Network(ws); ok {
				add(n)
			}
		}
		i.Drives = map[string]string{"/mnt/c": "c"}
		for _, what := range []string{PlaceHome, PlaceStorage, PlaceWatched, PlaceLinked} {
			if n, ok := i.SideNote(Place{What: what, Path: "/mnt/c/MetaRush"}); ok {
				add(n)
			}
		}
	}
	says = append(says, SlowFiles(1), SlowFiles(4), FolderSlowly, SpeedTip)
	for _, e := range []*PathError{} {
		says = append(says, e.Plain, e.Next)
	}
	i := Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Drives: map[string]string{"/mnt/c": "c"}}
	for _, typed := range []string{`E:\x`, `\\wsl.localhost\Debian\x`, `\\nas\share\x`} {
		_, err := i.LinuxPath(typed)
		pe := err.(*PathError)
		says = append(says, pe.Plain, pe.Next)
	}
	for _, s := range says {
		if s == "" {
			continue
		}
		if f := plainwords.Check(s); len(f) > 0 {
			t.Errorf("%q: %v", s, f)
		}
	}
}
