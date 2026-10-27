# English captions by whisper.cpp

Translates the speech of a clip in another language into English captions on this computer, and keeps them with
the clip: `metarush footage captions <clip> --language en` shows them (`--save english.srt` writes the file). It
wraps what MetaRush already uses: ffmpeg turns the clip's sound into 16 kHz mono, and whisper.cpp's `whisper-cli`
translates it (`-tr`) into SRT captions. The captions are marked as machine-translated.

They are the clip's, beside its transcript: a cut's own captions, and the translations Claude writes for them line
by line (`captions translate`), don't change. Use them to read what a clip says, as a reference for a translation,
or as a captions file for the clip itself.

- **What it needs on each computer** (a person sets it up):
  `metarush ability setup english-captions --file whisper-model=/full/path/ggml-medium.bin`
  - `ffmpeg` and `whisper-cli` (whisper.cpp; Homebrew: `brew install ffmpeg whisper-cpp`), pinned by fingerprint.
  - A whisper.cpp model **that can translate**: `ggml-medium.bin` (1.5 GB, a good balance), `ggml-small.bin`
    (faster, rougher) or `ggml-large-v3.bin` (3 GB, best). The turbo model MetaRush uses to write words down
    (`ggml-large-v3-turbo.bin`) was not trained to translate and only writes the language it hears; English-only
    models (`*.en.bin`) can't either. Download one from the whisper.cpp project's models (its
    `models/download-ggml-model.sh medium`) anywhere you like, then set it up as above. MetaRush pins it by its
    fingerprint and copies it into each run, read-only (an APFS clone on a Mac, so no extra space); the ability
    never reads it in place.
- **Licences:** whisper.cpp is MIT; OpenAI's Whisper models are MIT. This ability's code is MIT.
- **Cost:** free; it runs on this computer, **on the CPU**: MetaRush's sandbox doesn't open the GPU to abilities
  (whisper.cpp's Metal start-up is refused there), so it runs whisper-cli with `-ng`. That is slower than MetaRush's
  own transcription: on a recent Mac, expect around a minute per minute of speech with `ggml-small.bin` and several
  with `ggml-medium.bin` or `ggml-large-v3.bin`; a run may take up to an hour.
- **Key:** none.
- **What leaves this computer:** nothing; its network is off.
- **Who starts a run:** anyone, and Claude as a safe step: it is free, local and sends nothing.

Say the language (`language: es`) when you know it; otherwise whisper.cpp listens first. Captions come one
sentence at a time, as whisper.cpp hears them.
