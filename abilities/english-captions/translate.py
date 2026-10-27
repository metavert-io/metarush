"""English captions for a clip spoken in another language, by whisper.cpp on this computer.

Wraps what MetaRush already allows: ffmpeg makes 16 kHz mono sound from the clip and
whisper-cli translates its speech into English (-tr), writing SRT captions. Runs in
MetaRush's sandbox with the network off; the model is a file a person set up on this
computer, which MetaRush copies into the run ($METARUSH_FILE_WHISPER_MODEL).
"""
import json
import os
import re
import shutil
import struct
import subprocess
import sys

LANG_RE = re.compile(r"^[a-z]{2,3}$")


def program(name):
    p = shutil.which(name)
    if not p:
        sys.exit("%s isn't set up for this ability on this computer." % name)
    return p


SIZES = {384: "tiny", 512: "base", 768: "small", 1024: "medium", 1280: "large"}


def model_check(path):
    """Read the model's header: refuse one that can't translate, and say which it is.

    whisper.cpp models start with "ggml" and n_vocab, n_audio_ctx, n_audio_state,
    n_audio_head, n_audio_layer, n_text_ctx, n_text_state, n_text_head, n_text_layer…
    English-only models have a vocabulary of 51864; turbo has 4 text layers. MetaRush
    names the copy whisper-model.bin, so the name can't tell.
    """
    with open(path, "rb") as f:
        head = f.read(48)
    if len(head) < 48 or struct.unpack_from("<I", head)[0] != 0x67676D6C:
        sys.exit("The model set up for this ability isn't a whisper.cpp model.")
    vocab, _, audio_state, _, _, _, _, _, text_layers = struct.unpack_from("<9i", head, 4)
    if vocab == 51864:
        sys.exit("The model set up for this ability is English-only and can't translate. Set up ggml-medium.bin or ggml-large-v3.bin.")
    if text_layers == 4:
        sys.exit("The model set up for this ability is a turbo model, which writes the language it hears instead of translating. Set up ggml-medium.bin or ggml-large-v3.bin.")
    return "whisper.cpp %s (multilingual)" % SIZES.get(audio_state, "model")


def run(argv, what):
    r = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if r.returncode != 0:
        print(r.stdout[-4000:])
        sys.exit("%s stopped with an error (status %d)." % (what, r.returncode))
    return r.stdout


def main():
    ffmpeg, whisper = program("ffmpeg"), program("whisper-cli")
    if os.environ.get("METARUSH_TEST"):
        # The check has no clip and no model: it shows both programs run in the sandbox.
        print(run([ffmpeg, "-hide_banner", "-version"], "ffmpeg").splitlines()[0])
        out = subprocess.run([whisper, "--help"], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True).stdout
        if "--translate" not in out:
            sys.exit("this whisper-cli has no --translate")
        print("whisper-cli runs here and can translate")
        return
    with open(os.environ["METARUSH_INPUTS"]) as f:
        inputs = json.load(f)
    clip = inputs.get("clip")
    if not clip:
        sys.exit("Choose the clip whose speech to translate.")
    lang = str(inputs.get("language") or "auto").strip().lower()
    if lang != "auto" and not LANG_RE.match(lang):
        sys.exit("Say the language as a two- or three-letter code, like es or pt.")
    if lang == "en":
        sys.exit("The clip is in English already; its transcript is its captions.")
    model = os.environ.get("METARUSH_FILE_WHISPER_MODEL", "")
    if not model:
        sys.exit("No whisper.cpp model is set up for this ability on this computer.")
    made_with = model_check(model)
    tmp = os.environ.get("TMPDIR") or "/tmp"
    sound = os.path.join(tmp, "speech.wav")
    run([ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", clip, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", sound], "ffmpeg")
    stem = os.path.join(tmp, "captions")
    threads = str(max(1, min(8, (os.cpu_count() or 4))))
    # -ng: on the CPU. The sandbox doesn't open the GPU to abilities, and whisper.cpp's
    # Metal start-up stops there with an error.
    said = run([whisper, "-m", model, "-f", sound, "-tr", "-l", lang, "-osrt", "-of", stem, "-t", threads, "-np", "-ng"], "whisper-cli")
    srt = stem + ".srt"
    if not os.path.exists(srt) or not open(srt, encoding="utf-8", errors="replace").read().strip():
        print(said[-2000:])
        sys.exit("whisper.cpp heard no speech to translate.")
    out = os.environ["METARUSH_OUTPUT"]
    shutil.copyfile(srt, os.path.join(out, "english.srt"))
    lines = sum(1 for l in open(srt, encoding="utf-8", errors="replace") if "-->" in l)
    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump({"files": [{"path": "english.srt", "title": "English captions", "language": "en", "translated": True,
                              "model": "%s, translated from %s" % (made_with, "the language it heard" if lang == "auto" else lang)}]}, f)
    print("wrote %d lines of English captions" % lines)


if __name__ == "__main__":
    main()
