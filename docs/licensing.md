# Licensing

## MetaRush
MIT, © 2026 Metavert LLC (see `LICENSE`).

## Remotion — read this before installing for a company
MetaRush renders with [Remotion](https://www.remotion.dev), which is **not** MIT-licensed. As checked on 2026-10-17:

- Free for **individuals**, **for-profit organizations with up to 3 employees** (the FAQ phrases it as "a team of
  individuals with up to 3 people"), non-profits, and evaluation.
- Larger companies need a **Remotion Company License**. Published pricing: *Creators* $25/month per person who
  writes Remotion code; *Automators* $0.01 per render with a $100/month minimum.
- Remotion does not allow letting end users upload their own Remotion code to a rendering service. MetaRush only
  renders its own vetted templates with validated parameters.
- **Each organization that installs MetaRush is responsible for its own Remotion license.** MetaRush does not
  grant or sublicense Remotion.

Sources: https://www.remotion.dev/docs/license, https://www.remotion.dev/docs/license/faq, Remotion's LICENSE.md.
Check them again before relying on this summary.

## Other components
| Component | License | Notes |
|---|---|---|
| FFmpeg | LGPL/GPL (build-dependent) | Invoked as an external program; not bundled. Homebrew builds include GPL codecs (x264). |
| whisper.cpp | MIT | External binary. |
| Whisper model weights | MIT | Downloaded by `bootstrap.sh`. |
| **Parakeet TDT 0.6B v2 weights** (default transcriber) | **CC-BY-4.0** | Attribution required: *Speech recognition uses NVIDIA Parakeet TDT 0.6B v2 (https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2), licensed under CC BY 4.0, converted to ONNX by the sherpa-onnx project.* |
| pyannote segmentation 3.0 (ONNX) | MIT | Speaker diarization. |
| NVIDIA TitaNet-Small (ONNX) | Apache-2.0 (NeMo) | Speaker embeddings (diarization and the voice fingerprints that recognize people). |
| sherpa-onnx | Apache-2.0 | External binaries, plus its C library loaded at run time for voice fingerprints, and the onnxruntime it ships (MIT) for the voice clarity score and face fingerprints. |
| purego (`github.com/ebitengine/purego`) | Apache-2.0 | Go module that loads the sherpa-onnx C library without cgo. |
| DeepFilterNet 0.5.6 (`deep-filter` CLI and its built-in model) | MIT OR Apache-2.0 | Clearer voices, clean mode. Downloaded (pinned release binary, SHA-256 checked) into `<home>/components` on first use; not bundled. *Uses DeepFilterNet by Hendrik Schröter (https://github.com/Rikorose/DeepFilterNet).* |
| DNSMOS P.835 model (`sig_bak_ovr.onnx`, microsoft/DNS-Challenge) | Repository CC BY 4.0, code MIT | The voice clarity score. Downloaded at a pinned commit. Attribution: *Voice clarity scores use Microsoft's DNSMOS P.835 (Reddy et al., "DNSMOS P.835", ICASSP 2022), from https://github.com/microsoft/DNS-Challenge, CC BY 4.0.* |
| SFace face recognition model (`face_recognition_sface_2021dec.onnx`, OpenCV Zoo) | Apache-2.0 ("All files in this directory are licensed under Apache 2.0 License", model files included; contributed by the paper's author) | Recognizing faces across clips. Downloaded on first use from OpenCV's Hugging Face mirror at a pinned revision, SHA-256 checked (docs/installation.md); not bundled. Run through onnxruntime (MIT) from the sherpa-onnx install. *Uses SFace (Zhong, Deng, Hu, Zhao, Li, Wen, "SFace: Sigmoid-Constrained Hypersphere Loss for Robust Face Recognition", IEEE TIP 2021) from https://github.com/opencv/opencv_zoo.* The weights' training data is not stated by OpenCV Zoo. |
| Resemble Enhance (code and model) | MIT | Clearer voices, studio mode (optional, set up by a person). Installed with pip into its own environment; the model is downloaded from Hugging Face (ResembleAI/resemble-enhance) at a pinned revision. |
| Studio's Python packages | BSD (PyTorch, torchaudio, NumPy, SciPy, pandas, omegaconf, soundfile), ISC (librosa), MIT (rich, tabulate), MPL-2.0/MIT (tqdm), Matplotlib license | Installed from PyPI on the person's machine at studio setup; MetaRush does not redistribute them. |
| Fonts in `video/` | SIL Open Font License | Bundled via @fontsource. |
| Go/npm dependencies | MIT/BSD/Apache-2.0 | See `go.mod`, `package-lock.json`. |

## Your media
Music and third-party footage need rights. The edit spec records `rights` for audio tracks and `doctor`/preflight
warn when music has none. MetaRush never downloads media from the internet on its own.
