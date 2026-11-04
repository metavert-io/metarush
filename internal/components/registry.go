package components

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

const (
	mac   = "darwin"
	linux = "linux"
)

// remotionLicence is said whenever the renderer is added (bootstrap says the same).
const remotionLicence = "Remotion, which renders your videos, is free for individuals and for companies of up to 3 people; " +
	"larger companies need a Remotion Company License (remotion.dev/license) before using it. MetaRush itself is MIT-licensed."

// registry is every component: the required ones first, then the optional
// ones roughly in the order setup offers them.
var registry = []*Component{
	{
		Name: "metarush", Title: "MetaRush itself", Thing: "MetaRush", Feature: "Everything", Required: true,
		Unlocks:  "The dashboard, your projects, cut sheets, versions and everything else that needs no other program.",
		Features: []string{"The dashboard", "Projects and versions", "Cut sheets"},
		detect: func(_ context.Context, e *Env) Found {
			return Found{Ready: true, Version: version.Current().Version, Path: e.Tools.Path(tools.Self)}
		},
	},
	{
		Name: "database", Title: "A place for your projects", Thing: "a database for your projects", Feature: "Keeping your projects", Required: true,
		Unlocks:  "Where MetaRush keeps your projects, versions and notes: a database on this computer that setup downloads, or one you already have.",
		Features: []string{"Projects", "Working on several computers"}, DownloadMB: 85, Minutes: 1,
		detect: detectDatabase,
		ways:   []Way{{Says: "MetaRush's setup sets it up.", Command: "metarush setup"}},
	},
	{
		Name: "storage", Title: "A storage folder", Thing: "a storage folder", Feature: "Keeping your footage", Required: true,
		Unlocks:  "Where your footage, preview copies and finished videos are kept: a folder on this computer, or a cloud bucket.",
		Features: []string{"Footage", "Finished videos"},
		detect:   detectStorage,
		ways:     []Way{{Says: "MetaRush's setup sets it up.", Command: "metarush setup"}},
	},
	{
		Name: "ffmpeg", Title: "FFmpeg", Thing: "FFmpeg", Feature: "Bringing in footage", Suggested: true,
		Unlocks:    "Brings footage in: preview copies, thumbnails, waveforms, scene and pause finding, fixes, recordings and the check of every finished video.",
		Features:   []string{"Bringing in footage", "Preview copies", "Quality checks", "Fixes"},
		DownloadMB: 45, DiskMB: 160, Minutes: 2, Tools: []string{tools.FFmpeg, tools.FFprobe},
		detect: func(ctx context.Context, e *Env) Found {
			f := Found{Ready: e.Tools.Has(tools.FFmpeg) && e.Tools.Has(tools.FFprobe), Path: e.Tools.Path(tools.FFmpeg)}
			switch {
			case f.Ready:
				f.Version = e.Tools.Version(ctx, tools.FFmpeg)
			case e.Tools.Has(tools.FFmpeg):
				f.Partly = "FFmpeg is here without FFprobe, which reads your footage."
			}
			return f
		},
		ways: []Way{
			{Brew: []string{"ffmpeg"}},
			{Systems: []string{mac}, Pins: []string{"ffmpeg", "ffprobe"}},
			{Systems: []string{linux}, Says: "Install FFmpeg with your system's packages.", Command: "sudo apt install ffmpeg (or your system's package of it)"},
		},
		remove:     removePins("ffmpeg", "ffprobe"),
		removeSays: "It came from Homebrew or your system's packages; remove it there if nothing else uses it.",
	},
	{
		Name: "node", Title: "Node.js", Thing: "Node.js", Feature: "Rendering", Suggested: true,
		Unlocks:    "Runs the renderer, motion graphics checks, Remotion projects and web page snapshots; link imports use it for YouTube when Deno isn't there.",
		Features:   []string{"Finished videos", "Motion graphics", "Web page snapshots"},
		DownloadMB: 50, DiskMB: 200, Minutes: 1, Tools: []string{tools.Node},
		detect: func(ctx context.Context, e *Env) Found {
			if !e.Tools.Has(tools.Node) {
				return Found{}
			}
			v := e.Tools.Version(ctx, tools.Node)
			f := Found{Ready: true, Version: v, Path: e.Tools.Path(tools.Node)}
			if m := major(v); m > 0 && m < 18 {
				f.Ready, f.Partly = false, "The Node.js here is too old (MetaRush needs 22 or newer)."
			}
			return f
		},
		ways: []Way{
			{Brew: []string{"node"}},
			{Pins: []string{"node"}},
			{Says: "Install Node.js 22 or newer from nodejs.org.", Command: "https://nodejs.org/en/download"},
		},
		remove:     removePins("node"),
		removeSays: "It came from Homebrew or another installer; remove it there if nothing else uses it.",
	},
	{
		Name: renderer.Component, Title: "The Remotion renderer", Thing: "the Remotion renderer", Feature: "Rendering", Suggested: true,
		Unlocks:    "Makes finished videos and review copies at full quality in every shape, draws motion graphics and renders Remotion projects.",
		Features:   []string{"Finished videos", "Review copies", "Motion graphics", "Remotion projects"},
		DownloadMB: 400, DiskMB: 700, Minutes: 5, Licence: remotionLicence, Needs: []string{"node", "ffmpeg"},
		detect: func(_ context.Context, e *Env) Found {
			st := renderer.New(e.Cfg, e.Tools).Check(false)
			f := Found{Ready: st.Ready, Version: st.Remotion, Path: st.Dir, Detail: st.State}
			if !st.Ready && st.State != renderer.StateMissing && st.State != renderer.StateUnavailable && st.State != renderer.StateLeftOut {
				f.Partly = strings.TrimSpace(st.Problem)
			}
			return f
		},
		ways:   []Way{{Step: "renderer"}},
		remove: removeRenderer,
	},
	{
		Name: "transcription", Title: "Transcription on this computer", Thing: "the transcriber and its speech models", Feature: "Making transcripts", Suggested: true,
		Unlocks:    "Transcripts and captions made on this computer as footage arrives, nothing uploaded: search what anyone said, cut by deleting words, and tell voices apart.",
		Features:   []string{"Transcripts", "Captions", "Cutting by words", "Telling voices apart"},
		DownloadMB: 550, DiskMB: 700, Minutes: 5, Needs: []string{"ffmpeg"}, Tools: []string{tools.Sherpa, tools.SherpaDiar},
		detect: func(_ context.Context, e *Env) Found {
			p := transcribe.Select(e.Cfg.Transcription, e.Tools)
			if p == nil {
				return Found{}
			}
			f := Found{Ready: true, Version: p.Name() + " " + p.Model(), Path: e.Tools.Path(tools.Sherpa)}
			if transcribe.NewDiarizer(e.Tools) == nil {
				f.Partly = "Transcripts are made here, but voices aren't told apart yet."
				f.Detail = "no speaker models"
			}
			return f
		},
		ways: []Way{
			{Pins: []string{"sherpa-onnx", "parakeet", "speaker-segments", "speaker-voices"}},
			{Brew: []string{"whisper-cpp"}, Pins: []string{"whisper-model"}},
			{Says: "Build whisper.cpp (github.com/ggml-org/whisper.cpp) and put whisper-cli on the PATH; then add this again and MetaRush downloads its speech model.",
				Command: "https://github.com/ggml-org/whisper.cpp#quick-start"},
		},
		remove: removePins("sherpa-onnx", "parakeet", "speaker-segments", "speaker-voices"),
	},
	{
		Name: "other-languages", Title: "Transcripts in other languages", Thing: "whisper.cpp and its speech model", Feature: "Making transcripts in other languages",
		Unlocks:    "Transcripts and captions in about a hundred languages with whisper.cpp, and telling which language a clip is in.",
		Features:   []string{"Transcripts in other languages", "Finding a clip's language"},
		DownloadMB: 1625, DiskMB: 1650, Minutes: 8, Needs: []string{"ffmpeg"}, Tools: []string{tools.Whisper},
		detect: func(_ context.Context, e *Env) Found {
			p := transcribe.Select(config.Transcription{Provider: "whispercpp", Model: e.Cfg.Transcription.Model}, e.Tools)
			if p == nil {
				f := Found{}
				if e.Tools.Has(tools.Whisper) {
					f.Partly = "whisper.cpp is here, but its speech model isn't."
				}
				return f
			}
			return Found{Ready: true, Version: p.Model(), Path: e.Tools.Path(tools.Whisper)}
		},
		ways: []Way{
			{When: func(e *Env) bool { return e.Tools.Has(tools.Whisper) }, Pins: []string{"whisper-model"}},
			{Brew: []string{"whisper-cpp"}, Pins: []string{"whisper-model"}},
			{Says: "Build whisper.cpp (github.com/ggml-org/whisper.cpp) and put whisper-cli on the PATH; then add this again and MetaRush downloads its speech model.",
				Command: "https://github.com/ggml-org/whisper.cpp#quick-start"},
		},
		remove:     removePins("whisper-model"),
		removeSays: "whisper.cpp came from Homebrew or a build of your own; remove it there if nothing else uses it.",
	},
	{
		Name: "link-import", Title: "The link downloader (yt-dlp)", Thing: "the link downloader (yt-dlp)", Feature: "Importing videos from links", Suggested: true,
		Unlocks:    "Brings in videos from YouTube, TikTok, X and other links you paste.",
		Features:   []string{"Importing from links"},
		DownloadMB: 40, Minutes: 1, Tools: []string{tools.YtDlp, tools.Deno},
		detect: func(ctx context.Context, e *Env) Found {
			if !e.Tools.Has(tools.YtDlp) {
				return Found{}
			}
			f := Found{Ready: true, Version: e.Tools.Version(ctx, tools.YtDlp), Path: e.Tools.Path(tools.YtDlp)}
			if !e.Tools.Has(tools.Deno) && !e.Tools.Has(tools.Node) {
				f.Partly = "YouTube imports also need Deno or Node.js."
			}
			return f
		},
		ways: []Way{
			{Brew: []string{"yt-dlp"}},
			{Pins: []string{"yt-dlp"}},
		},
		remove:     removePins("yt-dlp"),
		removeSays: "It came from Homebrew or pip; remove it there if nothing else uses it.",
	},
	{
		Name: "pdf-pages", Title: "PDF pages (poppler)", Thing: "poppler", Feature: "Showing the pages of a PDF", Suggested: true,
		Unlocks:    "Shows the pages of a PDF in your videos, like slides from a deck.",
		Features:   []string{"PDF pages in videos"},
		DownloadMB: 30, Minutes: 1, Tools: []string{tools.Pdftoppm, tools.Pdfinfo},
		detect: func(_ context.Context, e *Env) Found {
			return Found{Ready: e.Tools.Has(tools.Pdftoppm) && e.Tools.Has(tools.Pdfinfo), Path: e.Tools.Path(tools.Pdftoppm)}
		},
		ways: []Way{
			{Brew: []string{"poppler"}},
			{Systems: []string{linux}, Says: "Install poppler's tools with your system's packages.", Command: "sudo apt install poppler-utils (or your system's package of it)"},
			{Systems: []string{mac}, Says: "Install Homebrew (brew.sh), then add this again.", Command: "https://brew.sh"},
		},
		removeSays: "It came from Homebrew or your system's packages; remove it there if nothing else uses it.",
	},
	{
		Name: looker.Component, Title: "Seeing what's on screen", Thing: screenThing, Feature: "Seeing what's on screen", Suggested: true,
		Unlocks:    "Looks at every clip on this computer, nothing uploaded: faces and people, the words on screen, and framing that follows faces; on a Mac Apple Vision also names places, things and animals and sees gestures.",
		Features:   []string{"Faces and people", "Words on screen", "Framing that follows faces"},
		DownloadMB: screenDownloadMB, DiskMB: screenDiskMB, Minutes: 1, Needs: []string{"ffmpeg", "transcription"}, Tools: []string{tools.Vision, tools.Swiftc},
		here: func(e *Env) Here {
			if e.looksWith() == looker.AppleVision {
				return Here{Needs: []string{"ffmpeg"}, DiskMB: 5,
					Unlocks: "Looks at every clip with Apple Vision on this Mac, nothing uploaded: people and faces, places, things and words on screen, and framing that follows faces."}
			}
			return Here{Needs: []string{"ffmpeg", "transcription"}, DownloadMB: openModelsMB,
				Unlocks: "Looks at every clip with open models on this computer, nothing uploaded: faces and people, the words on screen (Latin, Greek, Chinese and Japanese writing) and what stands out, and framing that follows faces.",
				Licence: "The open models are YuNet (MIT), PaddleOCR's text models (Apache-2.0) and U²-Net-p (Apache-2.0)."}
		},
		detect: func(ctx context.Context, e *Env) Found {
			st := looker.CheckBackend(ctx, e.Tools, e.looksWith())
			if st.Backend == looker.AppleVision {
				f := Found{Ready: st.Ready && !st.Stale, Path: st.Path, Detail: st.Text}
				if !st.Supported {
					f.NotHere = "It needs a Mac; a Mac in your workspace can look at your clips."
				}
				return f
			}
			f := Found{Ready: st.Ready, Path: st.Path, Detail: "the open models"}
			switch {
			case !st.Supported:
				f.NotHere = "It isn't made for this kind of computer yet; a Mac or a Linux computer in your workspace can look at your clips."
			case !st.Ready && visionmodels.StoreIn(e.Tools.ModelsDir()).Present(visionmodels.PartIndex):
				f.Partly = st.Text
			}
			return f
		},
		ways: []Way{
			{When: usesOpenModels, Pins: screenIndexPins()},
			{Systems: []string{mac}, When: func(e *Env) bool { return e.Tools.Has(tools.Swiftc) }, Step: "vision"},
			{Systems: []string{mac}, Says: "Install Apple's Command Line Tools (a window asks first), then add this again.", Command: "xcode-select --install"},
		},
		remove: removeScreenIndex,
	},
	{
		Name: "faces", Title: "Recognizing faces across clips", Thing: "the face model", Feature: "Recognizing faces across clips", Suggested: true,
		Unlocks:    "Recognizes the people you named in every clip, so you're asked once per person, not once per appearance.",
		Features:   []string{"Faces recognized across clips"},
		DownloadMB: 40, Minutes: 1, Needs: []string{looker.Component, "transcription"},
		detect: func(_ context.Context, e *Env) Found {
			model := faceid.Setup{Home: e.Home()}.Model()
			f := Found{Ready: model != "" && onnx.Library(e.Tools.Path(tools.Sherpa)) != "", Path: model}
			if model != "" && !f.Ready {
				f.Partly = "The face model is here, but the transcription program that runs it isn't."
			}
			if !onnx.Available {
				f.NotHere = "It isn't made for this kind of computer yet."
			}
			return f
		},
		ways:   []Way{{Step: "faces"}},
		remove: removeFile(func(e *Env) string { return faceid.Setup{Home: e.Home()}.Path() }),
	},
	{
		Name: looker.CutOutComponent, Title: "Cutting out a picture's subject", Thing: "the cut-out model", Feature: "Cutting out a picture's subject",
		Unlocks:    "Takes the background out of photos with an open model on this computer, nothing uploaded: Remove the background for pictures, and AI cut-outs.",
		Features:   []string{"Removing a photo's background", "AI cut-outs"},
		DownloadMB: cutOutMB, Minutes: 3, Needs: []string{"transcription"}, Licence: "The model is BiRefNet (MIT).",
		detect: func(_ context.Context, e *Env) Found {
			if e.looksWith() == looker.AppleVision {
				return Found{NotHere: "On this Mac, Apple Vision cuts a picture's subject out; it comes with Seeing what's on screen."}
			}
			st := looker.CheckCutOutBackend(e.Tools, looker.OpenModels)
			f := Found{Ready: st.Ready, Path: st.Path, Detail: st.Engine}
			if !onnx.Available {
				f.NotHere = "It isn't made for this kind of computer yet."
			}
			return f
		},
		ways:   []Way{{Pins: []string{looker.CutOutComponent}}},
		remove: removePins(looker.CutOutComponent),
	},
	{
		Name: "clean-voice", Title: "Clearer voices on this computer", Thing: "the voice cleaner", Feature: "Making voices clearer", Suggested: true,
		Unlocks:    "Takes room echo and background noise out of voices on this computer, free, and scores how clear they are.",
		Features:   []string{"Clearer voices", "Voice clarity scores"},
		DownloadMB: 30, Minutes: 1,
		ways: []Way{{Step: "clean-voice"}},
	},
	{
		Name: "stand-in-voice", Title: "A stand-in voice", Thing: "a stand-in voice (espeak-ng)", Feature: "Timing narration with a stand-in voice", Suggested: true,
		Unlocks:    "A free, robotic voice to time narration before you record it or choose an AI voice.",
		Features:   []string{"Timing narration"},
		DownloadMB: 5, Minutes: 1, Tools: []string{tools.EspeakNG, tools.Say},
		detect: func(_ context.Context, e *Env) Found {
			if e.goos() == mac && e.Tools.Has(tools.Say) {
				return Found{Ready: true, Path: e.Tools.Path(tools.Say), Detail: "built into macOS"}
			}
			return Found{Ready: e.Tools.Has(tools.EspeakNG), Path: e.Tools.Path(tools.EspeakNG)}
		},
		ways: []Way{
			{Systems: []string{linux}, Brew: []string{"espeak-ng"}},
			{Systems: []string{linux}, Says: "Install espeak-ng with your system's packages.", Command: "sudo apt install espeak-ng"},
			{Systems: []string{mac}, Says: "macOS has it built in; if it is missing, macOS needs repairing.", Command: "/usr/bin/say"},
		},
		removeSays: "It is built into macOS, or came from your system's packages.",
	},
	{
		Name: "sandbox", Title: "A sandbox for abilities", Thing: "a sandbox (bubblewrap)", Feature: "Running abilities", Suggested: true,
		Unlocks:    "Keeps abilities and a Remotion project's own steps away from your files and keys while they run.",
		Features:   []string{"Abilities", "Refreshing Remotion projects"},
		DownloadMB: 1, Tools: []string{tools.Bwrap, tools.SandboxExec},
		detect: func(_ context.Context, e *Env) Found {
			i := sandbox.Here(e.Tools)
			return Found{Ready: i.OK(), Detail: i.Kind, Partly: map[bool]string{true: "", false: i.Problem}[e.Tools.Has(tools.Bwrap) && !i.OK()]}
		},
		ways: []Way{
			{Systems: []string{linux}, Says: "Install bubblewrap with your system's packages.", Command: "sudo apt install bubblewrap (or your system's package of it)"},
			{Systems: []string{mac}, Says: "macOS has it built in; if it is missing, macOS needs repairing.", Command: "/usr/bin/sandbox-exec"},
		},
		removeSays: "It is part of your system.",
	},
	{
		Name: "studio-voice", Title: "Studio voices", Thing: "the studio voice cleaner", Feature: "Giving voices a studio sound",
		Unlocks:    "Makes voices sound recorded in a studio, on this computer: slower and stronger than clearer voices.",
		Features:   []string{"Studio voices"},
		DownloadMB: 1800, Minutes: 15, Needs: []string{"clean-voice", "transcription"},
		ways: []Way{{Step: "studio-voice"}},
	},
	{
		Name: "stabilizer", Title: "Steadying shaky footage", Thing: "the steadying tool", Feature: "Steadying shaky footage well",
		Unlocks:    "The better way to steady shaky footage (vid.stab); without it, a basic steadying is used. It also sets itself up the first time footage is steadied.",
		Features:   []string{"Steady copies of shaky footage"},
		DownloadMB: 25, Minutes: 1, Needs: []string{"ffmpeg"}, Tools: []string{tools.Stabilizer},
		detect: func(ctx context.Context, e *Env) Found {
			st := (&fixes.Stabilizer{Home: e.Home(), Tools: e.Tools}).Status(ctx)
			return Found{Ready: st.Ready, Path: st.Path, Detail: st.Engine}
		},
		ways: []Way{
			{When: func(e *Env) bool { return fixes.CanDownloadStabilizer() }, Step: "stabilizer"},
			{Systems: []string{linux}, Says: "Install an FFmpeg built with vid.stab (most Linux systems' packages are).", Command: "sudo apt install ffmpeg"},
		},
		remove: removeFile(func(e *Env) string { return filepath.Join(e.Home(), "components", "stabilizer") }),
	},
	{
		Name: "ai-models", Title: "AI models on this computer", Thing: "the AI models", Feature: "Running AI models on this computer",
		Unlocks:    "Free AI voices (Kokoro, Kitten) and picture upscaling that run on this computer, with nothing sent anywhere.",
		Features:   []string{"Offline AI voices", "Upscaling pictures"},
		DownloadMB: 410, Minutes: 4, Needs: []string{"transcription"}, Tools: []string{tools.SherpaTTS},
		detect: func(_ context.Context, e *Env) Found {
			s := localmodels.Store{Home: e.Home()}
			here := s.Here()
			ready := 0
			for _, p := range here {
				if s.Ready(p) == nil {
					ready++
				}
			}
			f := Found{Ready: len(here) > 0 && ready == len(here), Detail: fmt.Sprintf("%d of %d set up", ready, len(here))}
			if len(here) == 0 {
				f.NotHere = "No AI models run on this kind of computer yet."
			} else if ready > 0 && !f.Ready {
				f.Partly = "Some of the models are set up."
			}
			return f
		},
		ways:   []Way{{Step: "ai-models"}},
		remove: removeFile(func(e *Env) string { return filepath.Join(e.Home(), "components", "local-models") }),
	},
	{
		Name: "content-credentials", Title: "Content Credentials (c2patool)", Thing: "c2patool", Feature: "Signing delivered videos with Content Credentials",
		Unlocks:    "Puts Content Credentials into the videos you deliver: signed with your certificate, they say the video was made with MetaRush and which parts AI made or changed.",
		Features:   []string{"Content Credentials"},
		Licence:    "c2patool is the C2PA project's own tool, under the MIT or Apache-2.0 licence (your choice); MetaRush downloads it from the project's releases and never bundles it.",
		DownloadMB: 23, DiskMB: 64, Minutes: 1, Tools: []string{tools.C2PATool},
		detect: func(ctx context.Context, e *Env) Found {
			if !e.Tools.Has(tools.C2PATool) {
				return Found{}
			}
			f := Found{Ready: true, Version: e.Tools.Version(ctx, tools.C2PATool), Path: e.Tools.Path(tools.C2PATool)}
			if why := (c2pa.Tool{Tools: e.Tools}).Ready(ctx); why != "" {
				f.Ready, f.Partly = false, sentence(why)
			}
			return f
		},
		ways: []Way{
			{Pins: []string{"c2patool"}},
			{Says: "Build c2patool 0.28.3 from its source with Rust's cargo, and put it on the PATH; then add this again.", Command: "cargo install c2patool --version 0.28.3"},
		},
		remove:     removePins("c2patool"),
		removeSays: "It didn't come from MetaRush; remove it where it came from if nothing else uses it.",
	},
	{
		Name: "google-drive", Title: "Google Drive folders (rclone)", Thing: "rclone", Feature: "Reading Google Drive folders",
		Unlocks:    "Reads Google Drive folders without syncing them, through your own rclone and its Google sign-in.",
		Features:   []string{"Google Drive folders"},
		DownloadMB: 25, Minutes: 1, Tools: []string{tools.Rclone},
		detect: func(_ context.Context, e *Env) Found {
			return Found{Ready: e.Tools.Has(tools.Rclone), Path: e.Tools.Path(tools.Rclone)}
		},
		ways: []Way{
			{Brew: []string{"rclone"}},
			{Says: "Install rclone (rclone.org/install), then run rclone config and add your Google Drive.", Command: "https://rclone.org/install/"},
		},
		removeSays: "It came from Homebrew or rclone's own installer; remove it there if nothing else uses it.",
	},
	{
		Name: "github", Title: "GitHub repositories", Thing: "git and GitHub's gh", Feature: "Bringing in GitHub repositories",
		Unlocks:    "Brings in GitHub repositories for code slides and diagrams, and sends a Remotion project's changes back, with your own GitHub sign-in.",
		Features:   []string{"Code slides", "Repository pictures", "Sending changes back"},
		DownloadMB: 15, Minutes: 1, Tools: []string{tools.Git, tools.GH},
		detect: func(_ context.Context, e *Env) Found {
			f := Found{Ready: e.Tools.Has(tools.Git) && e.Tools.Has(tools.GH), Path: e.Tools.Path(tools.Git)}
			if e.Tools.Has(tools.Git) && !e.Tools.Has(tools.GH) {
				f.Partly = "Repositories come in, but their details and sending changes back need GitHub's own program (gh)."
			}
			return f
		},
		ways: []Way{
			{Brew: []string{"gh"}},
			{Says: "Install git and GitHub's own program (gh) with your system's packages, then sign in to GitHub with it.", Command: "https://cli.github.com"},
		},
		removeSays: "It came from Homebrew or your system's packages; remove it there if nothing else uses it.",
	},
	{
		Name: "diagrams", Title: "D2 diagrams", Thing: "D2", Feature: "Drawing D2 diagrams",
		Unlocks:    "Draws diagrams written in D2 for your videos (Mermaid diagrams need nothing extra).",
		Features:   []string{"D2 diagrams"},
		DownloadMB: 30, Minutes: 1, Tools: []string{tools.D2},
		detect: func(_ context.Context, e *Env) Found {
			return Found{Ready: e.Tools.Has(tools.D2), Path: e.Tools.Path(tools.D2)}
		},
		ways: []Way{
			{Brew: []string{"d2"}},
			{Says: "Install D2 from d2lang.com.", Command: "https://d2lang.com/tour/install"},
		},
		removeSays: "It came from Homebrew or D2's own installer; remove it there if nothing else uses it.",
	},
	{
		Name: "claude-code", Title: "Claude Code", Thing: "Claude Code", Feature: "Editing by asking",
		Unlocks:    "Edit by asking in plain words, in a terminal or the dashboard's panel; it uses your own Claude account.",
		Features:   []string{"Editing by asking", "Claude in the dashboard", "Scene descriptions"},
		DownloadMB: 200, Minutes: 2, Tools: []string{tools.Claude},
		detect: func(_ context.Context, e *Env) Found {
			// Only whether it is there: its version and sign-in are Claude
			// Code's to answer (firstrun asks it, D255).
			return Found{Ready: e.Tools.Has(tools.Claude), Path: e.Tools.Path(tools.Claude)}
		},
		ways: []Way{
			{Systems: []string{mac}, Cask: "claude-code"},
			{Says: "Install Claude Code (claude.com/claude-code), then open it once in a terminal and sign in.", Command: "npm install -g @anthropic-ai/claude-code"},
		},
		removeSays: "It came from Homebrew or Claude Code's own installer; remove it there.",
	},
	{
		Name: "tailscale", Title: "Tailscale", Thing: "Tailscale", Feature: "Opening MetaRush from your other computers",
		Unlocks:    "Opens this computer's MetaRush from your phone and other computers, privately, through your own Tailscale network.",
		Features:   []string{"Remote access"},
		DownloadMB: 50, Minutes: 3, Tools: []string{tools.Tailscale},
		detect: func(_ context.Context, e *Env) Found {
			return Found{Ready: e.Tools.Has(tools.Tailscale), Path: e.Tools.Path(tools.Tailscale)}
		},
		ways:       []Way{{Says: "Install Tailscale from tailscale.com and sign in; then Settings → People turns remote access on.", Command: "https://tailscale.com/download"}},
		removeSays: "Remove it the way it was installed.",
	},
}

// sentence starts words with a capital and ends them with a full stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// major is a version string's major number ("v22.14.0" → 22).
func major(v string) int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, ". "); i > 0 {
		v = v[:i]
	}
	n, _ := strconv.Atoi(v)
	return n
}

func detectDatabase(ctx context.Context, e *Env) Found {
	c := e.Cfg
	if l := c.Database.Local; l != nil {
		st := localdb.New(e.Home(), l.Port, e.Tools).Status(ctx)
		f := Found{Ready: st.SetUp, Version: st.Version, Path: st.Folder, Detail: fmt.Sprintf("on this computer, port %d", st.Port)}
		switch {
		case !st.SetUp:
			f.Partly = "The database on this computer isn't set up; run MetaRush's setup again."
		case st.CantStart != "":
			f.Partly = st.CantStart // this computer's Linux kernel stops it (D339)
		}
		return f
	}
	if c.Database.URI == "" {
		return Found{}
	}
	return Found{Ready: true, Detail: "elsewhere: " + config.HostOf(c.Database.URI)}
}

func detectStorage(_ context.Context, e *Env) Found {
	m := e.Cfg.Media
	switch m.Provider {
	case "s3", "gcs":
		return Found{Ready: true, Detail: m.Provider}
	}
	root := m.Filesystem.Root
	if root == "" {
		return Found{}
	}
	st, err := os.Stat(root)
	f := Found{Ready: err == nil && st.IsDir(), Path: root, Detail: "a folder"}
	if !f.Ready {
		f.Partly = "The storage folder isn't there; connect its drive or run MetaRush's setup again."
	}
	return f
}

// removePins removes what pins placed (only what MetaRush put there).
func removePins(ids ...string) func(ctx context.Context, e *Env) error {
	return func(_ context.Context, e *Env) error {
		for _, id := range ids {
			for _, p := range e.pinsFor(id) {
				if err := e.unfetch(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// removeFile removes a file or folder MetaRush downloaded or built.
func removeFile(path func(e *Env) string) func(ctx context.Context, e *Env) error {
	return func(_ context.Context, e *Env) error { return os.RemoveAll(path(e)) }
}

func removeVision(_ context.Context, e *Env) error {
	p := config.VisionToolPath(e.Home())
	_ = os.Remove(p + ".source-sha256")
	return os.RemoveAll(p)
}

// removeScreenIndex takes away what looks at the picture here: the helper
// MetaRush built, and the open models it downloaded.
func removeScreenIndex(ctx context.Context, e *Env) error {
	if err := removePins(screenIndexPins()...)(ctx, e); err != nil {
		return err
	}
	return removeVision(ctx, e)
}

// usesOpenModels: this computer looks at what's on screen with the open models.
func usesOpenModels(e *Env) bool { return e.looksWith() == looker.OpenModels }

// looksWith is how this computer looks at what's on screen (D408).
func (e *Env) looksWith() string { return looker.ChosenOn(e.Cfg, e.goos()) }

// The open models' downloads (D406), in megabytes.
var (
	openModelsMB = int((visionmodels.Store{}.Bytes(visionmodels.PartIndex) + 500_000) / 1_000_000)
	cutOutMB     = int((visionmodels.Store{}.Bytes(visionmodels.PartCutOut) + 500_000) / 1_000_000)
)

// What looks at the picture, as a missing component says it: on a Mac the
// helper MetaRush builds (Apple Vision), elsewhere the open models it
// downloads (a Mac that chose the open models sees their size in Settings →
// Components, through the component's here).
var screenThing, screenDownloadMB, screenDiskMB = func() (string, int, int) {
	if runtime.GOOS == mac {
		return "the on-screen helper", 0, 5
	}
	return "the open models that look at the picture", openModelsMB, 0
}()

// The open models' own words when a person left them out (looker.Open says
// them; one sentence, the component's).
func init() {
	looker.LeftOutWords = func() string { return Missing(looker.Component).Message }
}

// screenIndexPins are the open models' pins.
func screenIndexPins() []string {
	var out []string
	for _, f := range (visionmodels.Store{}).Part(visionmodels.PartIndex) {
		out = append(out, f.ID)
	}
	return out
}

// removeRenderer removes this version's renderer unless a render is using
// it (it says so plainly).
func removeRenderer(ctx context.Context, e *Env) error {
	return renderer.New(e.Cfg, e.Tools).RemoveCurrent(ctx)
}
