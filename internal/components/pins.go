package components

// Pin is one download MetaRush may make for a component: an address, the
// size and checksum its publisher states (or, where it states none, the one
// measured when it was pinned), and where its files go.
type Pin struct {
	ID    string
	Title string
	URL   string
	// SHA256 is checked before anything is unpacked or kept, and Bytes
	// with it: the download is exactly that size.
	SHA256 string
	Bytes  int64
	// ProgramSHA256 is the kept program's own checksum, as its publisher
	// states it, for an address that isn't versioned (osxexperts keeps one
	// zip name per FFmpeg series): a zip packed again around the same
	// program is taken; a different program is refused.
	ProgramSHA256 string
	// Platforms are GOOS/GOARCH (empty: any).
	Platforms []string
	// Take is the folder or file inside the archive that is kept ("" for a
	// download that is the file itself).
	Take string
	// To is where it goes: "tools/…" in the home, "models/…" in the models
	// folder (tools.modelsDir: large files every home on a computer shares).
	To string
	// Exec marks a downloaded file as a program.
	Exec bool
	// Config is the config.yaml tools setting pointed at ConfigPath (inside
	// To; "" To itself) once it is in place.
	Config, ConfigPath string
	// Licence is the download's licence, for Details.
	Licence string
	// Hosts are the other hosts the publisher's address redirects to
	// (GitHub's release assets, Hugging Face's CDN, evermeet's mirror); a
	// redirect anywhere else, or away from HTTPS, is refused.
	Hosts []string
}

// Where publishers send their downloads.
var (
	githubAssets = []string{"release-assets.githubusercontent.com", "objects.githubusercontent.com"}
	hfCDN        = []string{"us.aws.cdn.hf.co", "eu.aws.cdn.hf.co", "cdn-lfs.huggingface.co", "cdn-lfs-us-1.hf.co", "cdn-lfs-eu-1.hf.co", "cas-bridge.xethub.hf.co"}
	evermeet     = []string{"e.deolaha.ca"}
)

// For reports whether a pin is for this computer.
func (p Pin) For(goos, goarch string) bool {
	if len(p.Platforms) == 0 {
		return true
	}
	for _, x := range p.Platforms {
		if x == goos+"/"+goarch {
			return true
		}
	}
	return false
}

// The pinned downloads (D326). Checked 26 Oct 2026, each from its
// publisher's own page:
//   - Node.js: nodejs.org's SHASUMS256.txt for v22.23.4 (the current 22 LTS).
//   - yt-dlp 2026.10.21: the release's SHA2-256SUMS (GitHub states the same).
//   - sherpa-onnx 1.13.8 (its no-speech builds: the transcriber and speaker
//     programs with onnxruntime) and the Parakeet v2 model: the digests GitHub
//     states for the release files.
//   - The speaker models: TitaNet small as bootstrap pins it; pyannote's
//     model.onnx from Hugging Face at revision 9403a690 (its LFS checksum,
//     the file bootstrap pins).
//   - whisper.cpp's large-v3-turbo model: Hugging Face's LFS checksum at
//     revision 5359861c.
//   - FFmpeg for Macs without Homebrew: the builds the stabilizer pins
//     (osxexperts for Apple silicon, by the programs' published checksums;
//     evermeet for Intel, its sizes as its info API states them, FFprobe's
//     zip measured).
//
// Re-read the publisher's page and licence before changing one.
var pins = []Pin{
	// Node.js 22 (MIT).
	{ID: "node", Title: "Node.js 22.23.4", URL: "https://nodejs.org/dist/v22.23.4/node-v22.23.4-darwin-arm64.tar.gz",
		SHA256: "c9e1dbc748b33ed2d14f42445191ecaf14c56259c7a9a26d0042c49ec2b66428", Bytes: 49996369, Platforms: []string{"darwin/arm64"},
		Take: "node-v22.23.4-darwin-arm64", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"},
	{ID: "node", Title: "Node.js 22.23.4", URL: "https://nodejs.org/dist/v22.23.4/node-v22.23.4-darwin-x64.tar.gz",
		SHA256: "efcc2b0db6f94bf53eddb038fe2b370277a042bd29e4f19b8f8e23598795b819", Bytes: 51190214, Platforms: []string{"darwin/amd64"},
		Take: "node-v22.23.4-darwin-x64", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"},
	{ID: "node", Title: "Node.js 22.23.4", URL: "https://nodejs.org/dist/v22.23.4/node-v22.23.4-linux-x64.tar.gz",
		SHA256: "b8492284c76a54baf58d61668dd0e41ee0fc98adabf9503d30f05d08f55b4406", Bytes: 56697252, Platforms: []string{"linux/amd64"},
		Take: "node-v22.23.4-linux-x64", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"},
	{ID: "node", Title: "Node.js 22.23.4", URL: "https://nodejs.org/dist/v22.23.4/node-v22.23.4-linux-arm64.tar.gz",
		SHA256: "f7a1e486dbc8b286847815b477d4fb5e9fb963ed4489c3e1588853d685f589f9", Bytes: 56807066, Platforms: []string{"linux/arm64"},
		Take: "node-v22.23.4-linux-arm64", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"},

	// FFmpeg 9 for Macs without Homebrew (GPL builds; the same files the stabilizer pins). osxexperts' addresses
	// aren't versioned, so its programs are pinned by the SHA-256 its page publishes for each program; evermeet's
	// are versioned (its info API states the size, and it signs each zip with GPG: ffprobe-9.0.2.zip.sig).
	{ID: "ffmpeg", Title: "FFmpeg 9.0", URL: "https://www.osxexperts.net/ffmpeg9arm.zip",
		SHA256: "d0c06c5c68ce48af3143b262f7a9118a7c9f67de1e237fcc24ffb14df9c67af9", Bytes: 22608364, Platforms: []string{"darwin/arm64"},
		ProgramSHA256: "591260c945d0eef150e3bf82b0ef988bd36a9cecc18ff05d6679617159f0a95e", Take: "ffmpeg", To: "tools/ffmpeg/ffmpeg", Exec: true, Config: "ffmpeg", Licence: "GPL-3.0 (FFmpeg)"},
	{ID: "ffprobe", Title: "FFprobe 9.0", URL: "https://www.osxexperts.net/ffprobe9arm.zip",
		SHA256: "0c94fbdd8917022f28115eca512196cf4648732bc9e5db9ec8896c7e519d02aa", Bytes: 22530006, Platforms: []string{"darwin/arm64"},
		ProgramSHA256: "e11c17e8200b3ee4c4c186d245e2b4053f01d56957336c1817fca0b997469106", Take: "ffprobe", To: "tools/ffmpeg/ffprobe", Exec: true, Config: "ffprobe", Licence: "GPL-3.0 (FFmpeg)"},
	{ID: "ffmpeg", Title: "FFmpeg 9.0.2", URL: "https://evermeet.cx/ffmpeg/ffmpeg-9.0.2.zip",
		SHA256: "4acc0be580f9b2788029eb7bd4d645ff87968911b0a62aeeb3940d42d54558d5", Bytes: 26198325, Platforms: []string{"darwin/amd64"},
		Take: "ffmpeg", To: "tools/ffmpeg/ffmpeg", Exec: true, Config: "ffmpeg", Licence: "GPL-3.0 (FFmpeg)", Hosts: evermeet},
	{ID: "ffprobe", Title: "FFprobe 9.0.2", URL: "https://evermeet.cx/ffmpeg/ffprobe-9.0.2.zip",
		SHA256: "24a9c968cd4da72d99c7245e914b921815835eb6dff01d99868031aebaf1d439", Bytes: 26104193, Platforms: []string{"darwin/amd64"},
		Take: "ffprobe", To: "tools/ffmpeg/ffprobe", Exec: true, Config: "ffprobe", Licence: "GPL-3.0 (FFmpeg)", Hosts: evermeet},

	// The link downloader, as one self-updating program (Unlicense).
	{ID: "yt-dlp", Title: "yt-dlp 2026.10.21", URL: "https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/yt-dlp_macos",
		SHA256: "7a6fe71d7d680f1fb0ac18d4949873d77db2859e90017c0f44bd94b23be4ee40", Bytes: 37180736, Platforms: []string{"darwin/arm64", "darwin/amd64"},
		To: "tools/yt-dlp/yt-dlp", Exec: true, Config: "ytDlp", Licence: "Unlicense", Hosts: githubAssets},
	{ID: "yt-dlp", Title: "yt-dlp 2026.10.21", URL: "https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/yt-dlp_linux",
		SHA256: "d8bd33fca9d5b23bd5b9952b3fca36db5c46331933fda9f598a3da14ce2c243f", Bytes: 40569080, Platforms: []string{"linux/amd64"},
		To: "tools/yt-dlp/yt-dlp", Exec: true, Config: "ytDlp", Licence: "Unlicense", Hosts: githubAssets},
	{ID: "yt-dlp", Title: "yt-dlp 2026.10.21", URL: "https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.21/yt-dlp_linux_aarch64",
		SHA256: "e137c2171b0b8ef648e6136e86aa70c65923e93e434f01c93901d64744b190aa", Bytes: 40281264, Platforms: []string{"linux/arm64"},
		To: "tools/yt-dlp/yt-dlp", Exec: true, Config: "ytDlp", Licence: "Unlicense", Hosts: githubAssets},

	// The transcriber's program: sherpa-onnx without speech (Apache-2.0; onnxruntime MIT).
	{ID: "sherpa-onnx", Title: "sherpa-onnx 1.13.8", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-osx-arm64-shared-no-tts.tar.bz2",
		SHA256: "91b96512c4fa1960f8a9ed5360a6c8dda53a4b5015d0590244f14086a234557a", Bytes: 18252168, Platforms: []string{"darwin/arm64"},
		Take: "sherpa-onnx-v1.13.8-osx-arm64-shared-no-tts", To: "tools/sherpa-onnx", Config: "sherpaDir", Licence: "Apache-2.0", Hosts: githubAssets},
	{ID: "sherpa-onnx", Title: "sherpa-onnx 1.13.8", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-osx-x64-shared-no-tts.tar.bz2",
		SHA256: "03fd4cffd98b239d74b9253c270ff637661adb4d51a5a6c9e1f7486e48306db3", Bytes: 20646120, Platforms: []string{"darwin/amd64"},
		Take: "sherpa-onnx-v1.13.8-osx-x64-shared-no-tts", To: "tools/sherpa-onnx", Config: "sherpaDir", Licence: "Apache-2.0", Hosts: githubAssets},
	{ID: "sherpa-onnx", Title: "sherpa-onnx 1.13.8", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-linux-x64-shared-no-tts.tar.bz2",
		SHA256: "d0f96c8b65c6cd0974fada22737e337de81bc8cd2abbec2e39caf358b1eec5fc", Bytes: 24802494, Platforms: []string{"linux/amd64"},
		Take: "sherpa-onnx-v1.13.8-linux-x64-shared-no-tts", To: "tools/sherpa-onnx", Config: "sherpaDir", Licence: "Apache-2.0", Hosts: githubAssets},

	// The English speech model and the speaker models (any computer the program runs on).
	{ID: "parakeet", Title: "Parakeet TDT 0.6B v2 (int8)", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
		SHA256: "157c157bc51155e03e37d2466522a3a737dd9c72bb25f36eb18912964161e1ad", Bytes: 482468385,
		Take: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", To: "models/parakeet/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8", Licence: "CC-BY-4.0 (NVIDIA)", Hosts: githubAssets},
	{ID: "speaker-segments", Title: "pyannote segmentation 3.0", URL: "https://huggingface.co/csukuangfj/sherpa-onnx-pyannote-segmentation-3-0/resolve/9403a6902bb58e3d5ae8c7e77c3422de279db2e0/model.onnx",
		SHA256: "220ad67ca923bef2fa91f2390c786097bf305bceb5e261d4af67b38e938e1079", Bytes: 5992913,
		To: "models/diarization/sherpa-onnx-pyannote-segmentation-3-0/model.onnx", Licence: "MIT", Hosts: hfCDN},
	{ID: "speaker-voices", Title: "TitaNet small", URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx",
		SHA256: "ad4a1802485d8b34c722d2a9d04249662f2ece5d28a7a039063ca22f515a789e", Bytes: 40257283,
		To: "models/diarization/nemo_en_titanet_small.onnx", Licence: "CC-BY-4.0 (NVIDIA)", Hosts: githubAssets},

	// whisper.cpp's model for other languages (MIT).
	{ID: "whisper-model", Title: "whisper large-v3-turbo", URL: "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo.bin",
		SHA256: "1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69", Bytes: 1624555275,
		To: "models/whisper/ggml-large-v3-turbo.bin", Licence: "MIT", Hosts: hfCDN},
}

// pinsFor is every pin with an ID for this computer.
func (e *Env) pinsFor(id string) []Pin {
	all := pins
	if e.Pins != nil {
		all = e.Pins
	}
	var out []Pin
	for _, p := range all {
		if p.ID == id && p.For(e.goos(), e.goarch()) {
			out = append(out, p)
		}
	}
	return out
}

// havePins reports whether every ID has a pin for this computer.
func (e *Env) havePins(ids []string) bool {
	for _, id := range ids {
		if len(e.pinsFor(id)) == 0 {
			return false
		}
	}
	return true
}
