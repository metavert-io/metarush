package wsl

import (
	"fmt"
	"strings"
	"time"
)

// What MetaRush says about running in WSL, in plain words (D109): one set of
// words for setup, doctor, the dashboard's Getting started and Settings →
// General. Commands, files and settings' names are in each note's Command,
// which people see behind Details or --details.

// Report is MetaRush on this computer as WSL leaves it.
type Report struct {
	// Version is WSL's: 2, or 1 (which MetaRush refuses).
	Version int    `json:"version"`
	Distro  string `json:"distro,omitempty"`
	// Says is the headline ("MetaRush runs in Ubuntu on Windows (WSL 2).").
	Says  string `json:"says"`
	Notes []Note `json:"notes"`
}

// Note is one thing to know, ready or what to do about it.
type Note struct {
	Key string `json:"key" jsonschema:"enum=wsl-version,enum=background,enum=keep-running,enum=browser,enum=network,enum=files"`
	// Ready: nothing to do about it.
	Ready bool   `json:"ready"`
	Says  string `json:"says"`
	Next  string `json:"next,omitempty"`
	// Command is the technical step (Details).
	Command string `json:"command,omitempty"`
}

// Place is a folder MetaRush uses on this computer, for the note about the
// Windows side: What is home, storage, watched or linked.
type Place struct {
	What string
	Path string
}

// Place kinds.
const (
	PlaceHome    = "home"
	PlaceStorage = "storage"
	PlaceWatched = "watched"
	PlaceLinked  = "linked"
)

// DefaultIdle is how long WSL keeps a distribution running once its last
// window has closed, unless the Windows user's .wslconfig says otherwise
// ([general] instanceIdleTimeout, 15 seconds by default).
const DefaultIdle = 15 * time.Second

// WatchEvery is how often a watched or linked folder on the Windows side is
// looked through (D521): Windows' drives reach Ubuntu over a slower link, and
// Windows never tells Ubuntu that a file there changed, so MetaRush looks
// (as it does everywhere), less often than on Linux's own disk.
const WatchEvery = 5 * time.Second

// distroWord is the distribution as people call it ("Ubuntu", not WSL's
// "Ubuntu-24.04").
func (i Info) distroWord() string {
	for _, d := range []string{i.Name, i.Distro} {
		if d = strings.TrimSpace(d); d != "" {
			return d
		}
	}
	return "Linux"
}

// distroName is the name `wsl -d` takes, or a stand-in for it.
func (i Info) distroName() string {
	if i.DistroKnown && i.Distro != "" {
		return i.Distro
	}
	return "<distribution>"
}

// Headline is the report's first line.
func (i Info) Headline() string {
	switch i.Version {
	case 1:
		return "MetaRush is in " + i.distroWord() + " on Windows with WSL 1, which it can't run on: it needs WSL 2."
	case 2:
		return "MetaRush runs in " + i.distroWord() + " on Windows (WSL 2)."
	}
	return ""
}

// WSL1 is the refusal of WSL 1 (D516): plain words, what to do, the command.
func (i Info) WSL1() Note {
	d := i.distroWord()
	return Note{Key: "wsl-version", Says: d + " runs on WSL 1 here, where MetaRush's database and many of its tools don't work.",
		Next:    "Switch " + d + " to WSL 2 in Windows PowerShell, then run MetaRush's setup again (docs/wsl.md says how).",
		Command: "wsl --set-version " + i.distroName() + " 2   (in Windows PowerShell; then open " + d + " again and run: metarush setup)"}
}

// Background is the note about MetaRush starting by itself (D517): systemd
// runs, is turned on but WSL hasn't restarted since, or is off.
func (i Info) Background() Note {
	d := i.distroWord()
	switch {
	case i.Systemd:
		return Note{Key: "background", Ready: true, Says: d + " runs systemd, so MetaRush can start by itself in the background."}
	case i.SystemdAsked:
		return Note{Key: "background", Says: d + " is set to run systemd, but WSL hasn't restarted since, so MetaRush can't start by itself in the background yet.",
			Next:    "Restart WSL from Windows: shut it down in PowerShell, then open " + d + " again.",
			Command: "wsl --shutdown   (in Windows PowerShell; then open " + d + " again and run: metarush service install)"}
	}
	return Note{Key: "background", Says: d + " runs without systemd here, so MetaRush can't start by itself in the background: it starts when you open the dashboard.",
		Next: "To have it start by itself, turn systemd on in " + d + "'s WSL settings, then restart WSL (docs/wsl.md says how).",
		Command: "sudo nano /etc/wsl.conf   and add these two lines:\n[boot]\nsystemd=true\n" +
			"then in Windows PowerShell: wsl --shutdown\nthen in " + d + ": metarush service install"}
}

// KeepRunning is the note about WSL stopping the distribution once its
// last window closes (MetaRush with it), and the setting that keeps it.
func (i Info) KeepRunning(ws WindowsSettings) Note {
	d := i.distroWord()
	idle := DefaultIdle
	if ws.IdleTimeoutMS != nil {
		if *ws.IdleTimeoutMS < 0 {
			return Note{Key: "keep-running", Ready: true, Says: "WSL keeps " + d + " running after its last window closes, so MetaRush keeps running too."}
		}
		idle = time.Duration(*ws.IdleTimeoutMS) * time.Millisecond
	}
	return Note{Key: "keep-running", Says: fmt.Sprintf("Windows stops %s %s after its last window closes, and MetaRush stops with it.", d, afterWords(idle)),
		Next: "To keep MetaRush running with no window open, turn off WSL's idle shutdown in your Windows user's WSL settings, then restart WSL (docs/wsl.md says how).",
		Command: "In %UserProfile%\\.wslconfig (on Windows):\n[general]\ninstanceIdleTimeout=-1\n" +
			"then in Windows PowerShell: wsl --shutdown"}
}

// afterWords is "about 15 seconds", "right away"…
func afterWords(d time.Duration) string {
	switch {
	case d <= 0:
		return "right away"
	case d < 2*time.Minute:
		return fmt.Sprintf("about %d seconds", int(d.Round(time.Second)/time.Second))
	case d < 2*time.Hour:
		return fmt.Sprintf("about %d minutes", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("about %d hours", int(d.Round(time.Hour)/time.Hour))
}

// Browser is the note about opening the dashboard in Windows' browser (D518).
func (i Info) Browser() Note {
	if i.Interop {
		return Note{Key: "browser", Ready: true, Says: "The dashboard opens in your Windows browser, at an address only this PC can open."}
	}
	d := i.distroWord()
	return Note{Key: "browser", Says: d + " can't start Windows programs here, so MetaRush can't open your Windows browser for you.",
		Next:    "Open the address MetaRush prints in Edge or Chrome on this PC: it works from Windows as it is.",
		Command: "To let it open the browser again: [interop] enabled=true in /etc/wsl.conf, then wsl --shutdown in Windows PowerShell"}
}

// Network is the note about Windows' network settings for WSL (D519):
// mirrored mode still keeps MetaRush on this PC; localhost forwarding off
// keeps Windows' browser out. ok is false when there is nothing to say.
func (i Info) Network(ws WindowsSettings) (Note, bool) {
	switch {
	case ws.Networking == "mirrored":
		return Note{Key: "network", Ready: true, Says: "WSL shares Windows' network here (mirrored), and MetaRush still listens only on this PC: other computers can't open it."}, true
	case ws.LocalhostOff && ws.Networking != "mirrored":
		return Note{Key: "network", Says: "WSL doesn't pass this PC's own addresses on to " + i.distroWord() + ", so your Windows browser can't reach the dashboard.",
			Next:    "Turn that back on in your Windows user's WSL settings, then restart WSL (docs/wsl.md says how).",
			Command: "In %UserProfile%\\.wslconfig (on Windows): [wsl2] localhostForwarding=true, then wsl --shutdown in Windows PowerShell"}, true
	}
	return Note{}, false
}

// SideNote is the note about a folder of MetaRush's on the Windows side
// (D521), ok false when the folder is on Linux's own disk.
func (i Info) SideNote(p Place) (Note, bool) {
	l := i.Drive(p.Path)
	if l == "" {
		return Note{}, false
	}
	drive := strings.ToUpper(l) + ":"
	n := Note{Key: "files", Command: p.What + ": " + p.Path + " (" + drive + ")"}
	switch p.What {
	case PlaceHome:
		n.Says = "MetaRush's own folder is on the Windows side (" + drive + "), where Ubuntu reads and writes slowly."
		n.Next = "Keep MetaRush's folder in your Linux home folder, the place setup suggests."
	case PlaceStorage:
		n.Says = "MetaRush keeps your footage and finished videos on the Windows side (" + drive + "), where Ubuntu reads and writes them slowly."
		n.Next = "Move the storage into your Linux home folder: Settings → Storage moves it, and nothing is lost."
	case PlaceWatched:
		n.Says = "Your watched folder is on the Windows side (" + drive + "): " + FolderSlowly
		n.Next = SpeedTip
	default:
		n.Says = "A linked folder is on the Windows side (" + drive + "): " + FolderSlowly
		n.Next = SpeedTip
	}
	return n, true
}

// FolderSlowly says how a watched or linked folder on the Windows side is
// looked through: Windows never tells Ubuntu that files changed there, so
// MetaRush looks every few seconds, and reading from Windows is slower.
const FolderSlowly = "MetaRush looks in it for new files every few seconds, and bringing them in takes longer than from your Linux home folder."

// SpeedTip is what to do for speed.
const SpeedTip = "For speed, keep new footage in a folder in your Linux home folder."

// SlowFiles is said when files are added from the Windows side (D521).
func SlowFiles(n int) string {
	if n == 1 {
		return "This file is on the Windows side, which Ubuntu reads slowly, so it takes longer to add. For speed, copy footage into your Linux home folder first."
	}
	return fmt.Sprintf("%d of these files are on the Windows side, which Ubuntu reads slowly, so they take longer to add. For speed, copy footage into your Linux home folder first.", n)
}

// ReportFor is the report: the headline, then the notes — for WSL 1 only its
// refusal; for WSL 2 the background service, keeping it running, the browser,
// the network (when there is something to say) and the folders on the
// Windows side. nil outside WSL.
func ReportFor(i Info, ws WindowsSettings, places []Place) *Report {
	if !i.In() {
		return nil
	}
	r := &Report{Version: i.Version, Distro: i.Distro, Says: i.Headline(), Notes: []Note{}}
	if i.Version == 1 {
		r.Notes = append(r.Notes, i.WSL1())
		return r
	}
	r.Notes = append(r.Notes, i.Background(), i.KeepRunning(ws), i.Browser())
	if n, ok := i.Network(ws); ok {
		r.Notes = append(r.Notes, n)
	}
	seen := map[string]bool{}
	for _, p := range places {
		if p.Path == "" || seen[p.What+p.Path] {
			continue
		}
		seen[p.What+p.Path] = true
		if n, ok := i.SideNote(p); ok {
			r.Notes = append(r.Notes, n)
		}
	}
	return r
}

// HereReport is ReportFor this computer.
func HereReport(places []Place) *Report {
	i := Here()
	if !i.In() {
		return nil
	}
	return ReportFor(i, HereWindowsSettings(), places)
}

// Ready reports whether every note is ready.
func (r *Report) Ready() bool {
	if r == nil {
		return true
	}
	for _, n := range r.Notes {
		if !n.Ready {
			return false
		}
	}
	return true
}

// WithoutPaths is the report for someone who isn't an owner: the same words,
// without the folders' paths behind Details (they name the Windows user and
// MetaRush's folders). nil stays nil.
func (r *Report) WithoutPaths() *Report {
	if r == nil {
		return nil
	}
	c := *r
	c.Notes = make([]Note, len(r.Notes))
	for k, n := range r.Notes {
		if n.Key == "files" {
			n.Command = ""
		}
		c.Notes[k] = n
	}
	return &c
}

// Note finds a note by its key.
func (r *Report) Note(key string) (Note, bool) {
	if r == nil {
		return Note{}, false
	}
	for _, n := range r.Notes {
		if n.Key == key {
			return n, true
		}
	}
	return Note{}, false
}
