#!/usr/bin/env bash
# scripts/ci-onnxruntime.sh <folder>: onnxruntime for the Go tests on Linux CI (D413, .github/workflows/ci.yml).
# The open models that look at what's on screen, face fingerprints and their stand-in models run
# through onnxruntime (internal/onnx), which MetaRush finds in the transcriber's install (sherpa-onnx).
# CI takes the same build the transcription component downloads on Linux (internal/components/pins.go,
# linux/amd64; TestCIOnnxruntimeIsThePinnedOne keeps the two in step), checks its SHA-256 and keeps
# only the library. Prints the library's path: the workflow gives it to the tests as METARUSH_ORT_LIB.
set -euo pipefail
URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.8/sherpa-onnx-v1.13.8-linux-x64-shared-no-tts.tar.bz2"
SHA256="d0f96c8b65c6cd0974fada22737e337de81bc8cd2abbec2e39caf358b1eec5fc"
TAKE="sherpa-onnx-v1.13.8-linux-x64-shared-no-tts"

dir="${1:?usage: scripts/ci-onnxruntime.sh <folder>}"
mkdir -p "$dir"
lib="$dir/$TAKE/lib/libonnxruntime.so"
if [ -s "$lib" ]; then
  echo "$lib"
  exit 0
fi
sum() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }
archive="$dir/sherpa-onnx.tar.bz2"
curl -fsSL --retry 3 --retry-all-errors -o "$archive" "$URL"
got="$(sum "$archive")"
if [ "$got" != "$SHA256" ]; then
  echo "the download isn't the pinned build (SHA-256 $got)" >&2
  exit 1
fi
tar -xjf "$archive" -C "$dir" "$TAKE/lib/libonnxruntime.so"
rm -f "$archive"
echo "$lib"
exit 0
