package localmodels

// The pinned downloads (D280). Each address is the publisher's own. The
// checksums of sherpa-onnx's GitHub release assets are the SHA-256 GitHub
// states; Qualcomm AI Hub's export publishes none, so its SHA-256 is the one
// measured when it was pinned (25 Oct 2026). Re-read the release page and
// the licence before changing one.
var pins = []Pin{
	{ID: "voices-program", Title: "the offline voices' program", What: "sherpa-onnx's speech program, with speech built in",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-osx-arm64-shared.tar.bz2",
		SHA256: "b10e5c7e2c30ea03de9c442655d14860d9edc475c6251d58a8f5f06e913a1d56", Bytes: 20314448,
		Root: "sherpa-onnx-v1.13.8-osx-arm64-shared", Program: "bin/sherpa-onnx-offline-tts", Needs: []string{"lib"},
		Licence: "Apache-2.0 (sherpa-onnx; it runs eSpeak NG, GPL-3.0, as part of its own program)", LicenceURL: "https://github.com/k2-fsa/sherpa-onnx/blob/master/LICENSE",
		Platforms: []string{"darwin/arm64"}},
	{ID: "voices-program", Title: "the offline voices' program", What: "sherpa-onnx's speech program, with speech built in",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-linux-x64-shared.tar.bz2",
		SHA256: "c0bdb7907d3a74bba1d55d22bf4d9fa75586cf1530614ebe88a27b9118e015c4", Bytes: 28156791,
		Root: "sherpa-onnx-v1.13.8-linux-x64-shared", Program: "bin/sherpa-onnx-offline-tts", Needs: []string{"lib"},
		Licence: "Apache-2.0 (sherpa-onnx; it runs eSpeak NG, GPL-3.0, as part of its own program)", LicenceURL: "https://github.com/k2-fsa/sherpa-onnx/blob/master/LICENSE",
		Platforms: []string{"linux/amd64"}},
	{ID: "kokoro-v1", Title: "Kokoro v1.0", What: "an offline voice model: 41 voices in English, Spanish, French, Hindi, Italian and Portuguese",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kokoro-multi-lang-v1_0.tar.bz2",
		SHA256: "c5f7e2d2caf082bc1d20fb70334a61d99d20b484500aad32e7cf84c128ea3298", Bytes: 349906910,
		Root: "kokoro-multi-lang-v1_0", Needs: []string{"model.onnx", "voices.bin", "tokens.txt", "espeak-ng-data", "lexicon-us-en.txt", "lexicon-gb-en.txt"},
		Licence: "Apache-2.0", LicenceURL: "https://huggingface.co/hexgrad/Kokoro-82M"},
	{ID: "kitten-nano", Title: "Kitten nano v0.8", What: "a small offline voice model: 8 English voices",
		URL:    "https://github.com/k2-fsa/sherpa-onnx/releases/download/tts-models/kitten-nano-en-v0_8-int8.tar.bz2",
		SHA256: "6fa5be852612ce761094ba74ee6123b4fc4acfefa79bf64dc63acae4a83af2fd", Bytes: 31220690,
		Root: "kitten-nano-en-v0_8-int8", Needs: []string{"model.int8.onnx", "voices.bin", "tokens.txt", "espeak-ng-data"},
		Licence: "Apache-2.0", LicenceURL: "https://github.com/KittenML/KittenTTS"},
	{ID: "real-esrgan", Title: "Real-ESRGAN general x4v3", What: "a small model that makes pictures four times larger",
		URL:    "https://qaihub-public-assets.s3.us-west-2.amazonaws.com/qai-hub-models/models/real_esrgan_general_x4v3/releases/v0.65.0/real_esrgan_general_x4v3-onnx-float.zip",
		SHA256: "fc094329b24a00c936ebc1cc4c51bc59633e7dc36f4a553344342171cbd3c7cf", Bytes: 4540895,
		Root: "real_esrgan_general_x4v3-onnx-float", Needs: []string{"real_esrgan_general_x4v3.onnx", "real_esrgan_general_x4v3.data"},
		Licence: "BSD-3-Clause (Real-ESRGAN; this ONNX export is Qualcomm AI Hub's, which names the same licence)", LicenceURL: "https://github.com/xinntao/Real-ESRGAN/blob/master/LICENSE"},
}
