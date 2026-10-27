# Pictures from ComfyUI on this computer

Sends a text-to-picture graph (a checkpoint, your words and the words to avoid, a size, steps and a seed) to the
ComfyUI server running on this computer, waits for it, and brings the pictures back. Bring your own GPU and your own
models: nothing is downloaded and nothing leaves this computer.

- **ComfyUI is GPL-3.0 software you install and run yourself, beside MetaRush.** Nothing of it is bundled with
  MetaRush or in this folder; this ability only talks to it over HTTP. Install it from its own project (the ComfyUI
  desktop app, or `git clone` and its `requirements.txt` in a Python environment of yours), put a checkpoint in its
  `models/checkpoints` folder and start it on 127.0.0.1, port 8188 (its default).
- **The one thing on this computer it reaches**: an owner agrees, when approving it, that it may reach
  ComfyUI on port 8188 of the computer it runs on (`metarush ability approve comfyui-local --local-service`, or the
  approval question in Library → Abilities), after seeing the program listening there: Python's executable, the
  folder ComfyUI was started in and the script it runs (`main.py`), and that it answers `/system_stats` as ComfyUI
  does. Only that program is reached: plans show it, each run checks it as it starts and while it runs, and another
  program on the port — a plain `python3 -m http.server`, say — is refused. Moved or restarted from another folder,
  ComfyUI needs an owner to agree again (`metarush ability setup comfyui-local --agree-local` on that computer). The
  run's proxy then forwards plain HTTP to 127.0.0.1:8188 and records each request; every other port of this computer, every local network and the internet stay closed. ComfyUI itself
  runs outside MetaRush's sandbox with your rights, so whatever it can do (custom nodes included), a run may ask it
  to: that is why a person starts each run, and why only an owner agrees to it.
- **Another port:** a ComfyUI on another port needs a version of this ability whose `local` says that port, and an
  owner's approval of that version; the script reads the address MetaRush gives it (`$METARUSH_LOCAL_URL`), so only
  the manifest's line changes.
- **Licence of the pictures:** whatever your checkpoint's licence says; the pictures are AI-generated, so say so
  where you publish them. (MetaRush doesn't yet mark pictures an ability made as AI-generated in deliveries; AI services
 do, for the work they make.)
- **Licence of this ability's code:** MIT.
- **Cost:** free; your computer does the work.
- **Key:** none.
- **What leaves this computer:** nothing through MetaRush. Your words go to ComfyUI on this computer, which runs
  outside the sandbox: if you added nodes that reach the internet (API nodes, a model manager), they may send things
  on by themselves.
- **Who starts a run:** a person, each time.

What it doesn't do yet: pictures as input (image to image) and ComfyUI's WebSocket progress; a run waits up to 25
minutes, checking every second.
