"""MetaRush's studio voice runner: Resemble Enhance (MIT, resemble-ai/resemble-enhance)
on WAV files, on an NVIDIA graphics card (CUDA) or the Apple GPU (MPS) when
there is one that works, else the CPU.

MetaRush writes this file into its studio component and runs it with the
component's own Python, always with typed arguments (never a shell):

  python -I run.py --model-dir DIR --in IN.wav --out OUT.wav [--denoised SAFE.wav]
                   [--device auto|cuda|mps|cpu] [--nfe 64] [--solver midpoint] [--lambd 1.0] [--tau 0.5]
  python -I run.py --model-dir DIR --jobs JOBS.json      (a list of {"in", "out", "denoised"})
  python -I run.py --model-dir DIR --check

The model loads once for all jobs (MetaRush sends every recording of a run
at once). "denoised" also writes the model's denoiser output (no generative
step: it never changes words), which MetaRush uses wherever the generated
voice says a word differently.

A graphics card that can't be used falls back to the CPU, and says why: no
working CUDA or MPS, a card this torch build has no code for, a model that
does not load there, or a job that runs out of the card's memory (that job
runs again on the CPU, and so do the rest).

It prints one JSON object per line on stdout: {"type": "ready", ...} once the
model is loaded, then {"type": "done", "index": i, ...} per job with the
device and timings.
"""

import argparse
import json
import os
import sys
import time
import types


def say(kind, **fields):
    fields["type"] = kind
    print(json.dumps(fields), flush=True)


def stub_training_modules():
    """resemble_enhance imports deepspeed (a training dependency that does not
    install on macOS) at module level; inference never calls it."""
    ds = types.ModuleType("deepspeed")

    class DeepSpeedConfig:  # noqa: D401 - a stand-in
        def __init__(self, *args, **kwargs):
            pass

    ds.DeepSpeedConfig = DeepSpeedConfig
    ds.init_distributed = lambda *a, **k: None
    acc = types.ModuleType("deepspeed.accelerator")
    acc.get_accelerator = lambda: None
    rt = types.ModuleType("deepspeed.runtime")
    eng = types.ModuleType("deepspeed.runtime.engine")

    class DeepSpeedEngine:
        pass

    eng.DeepSpeedEngine = DeepSpeedEngine
    ut = types.ModuleType("deepspeed.runtime.utils")
    ut.clip_grad_norm_ = lambda *a, **k: None
    ds.accelerator, ds.runtime, rt.engine, rt.utils = acc, rt, eng, ut
    sys.modules.update({"deepspeed": ds, "deepspeed.accelerator": acc, "deepspeed.runtime": rt,
                        "deepspeed.runtime.engine": eng, "deepspeed.runtime.utils": ut})


def cuda_problem(torch):
    """Why CUDA can't run the model here ("" when it can): no card or driver,
    or a card this torch build has no code for (its compute capability is not
    in the build's list, and no PTX it could compile from)."""
    try:
        if not torch.cuda.is_available():
            return "no NVIDIA graphics card with a working driver"
        major, minor = torch.cuda.get_device_capability(0)
        archs = torch.cuda.get_arch_list()
    except Exception as e:  # a driver too old for this build raises here
        return "CUDA does not start: %s" % e
    sm = major * 10 + minor
    built = []
    for a in archs:
        kind, _, num = a.partition("_")
        if num.rstrip("af").isdigit():
            built.append((kind, int(num.rstrip("af"))))
    if ("sm", sm) in built or any(k == "compute" and n <= sm for k, n in built):
        return ""
    # A card of the same major generation runs code built for an older minor one.
    if any(k == "sm" and n // 10 == major and n <= sm for k, n in built):
        return ""
    return "this torch build has no code for the graphics card (compute capability %d.%d; it has %s)" % (major, minor, " ".join(archs))


def pick_device(torch, requested):
    """The device to run on and why a graphics card was not used ("" if it was
    or none was asked for). auto: CUDA, then MPS, then the CPU."""
    if requested == "cpu":
        return "cpu", ""
    why = []
    if requested in ("auto", "cuda"):
        problem = cuda_problem(torch)
        if not problem:
            return "cuda", ""
        if requested == "cuda" or torch.cuda.is_available():
            why.append(problem)
    if requested in ("auto", "mps"):
        mps = getattr(torch.backends, "mps", None)
        if mps is not None and mps.is_available():
            return "mps", "; ".join(why)
        if requested == "mps":
            why.append("the Apple GPU is not available")
    return "cpu", "; ".join(why)


def out_of_memory(err):
    s = str(err).lower()
    return "out of memory" in s or "outofmemory" in type(err).__name__.lower()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--model-dir", required=True)
    p.add_argument("--in", dest="inp")
    p.add_argument("--out")
    p.add_argument("--device", default="auto", choices=["auto", "cuda", "mps", "cpu"])
    p.add_argument("--nfe", type=int, default=64)
    p.add_argument("--solver", default="midpoint", choices=["midpoint", "rk4", "euler"])
    p.add_argument("--lambd", type=float, default=1.0)
    p.add_argument("--tau", type=float, default=0.5)
    p.add_argument("--denoised", help="also write the denoiser's output here")
    p.add_argument("--jobs", help="a JSON file listing jobs: [{\"in\": ..., \"out\": ..., \"denoised\": ...}]")
    p.add_argument("--denoise-only", action="store_true", help="only the denoiser (no generative enhancement)")
    p.add_argument("--check", action="store_true")
    a = p.parse_args()

    os.environ.setdefault("PYTORCH_ENABLE_MPS_FALLBACK", "1")
    stub_training_modules()
    import numpy as np
    import scipy.optimize
    import soundfile as sf
    import torch

    # resemble_enhance's sampler calls float() on fsolve's one-element array,
    # which NumPy 2.5 no longer allows; hand it the number.
    _fsolve = scipy.optimize.fsolve

    def fsolve(*args, **kwargs):
        r = _fsolve(*args, **kwargs)
        if not kwargs.get("full_output") and getattr(r, "shape", None) == (1,):
            return r[0]
        return r

    scipy.optimize.fsolve = fsolve

    # On the Apple GPU (MPS), a Conv1d with more than 65,536 output channels
    # returns wrong numbers (measured with torch 2.11: errors of order 1 above
    # 65,536, exact up to it). The vocoder's kernel predictor has 221,184, so
    # such convolutions run in chunks of output channels there.
    mps_max_channels = 65536
    conv1d_forward = torch.nn.Conv1d.forward

    def chunked_conv1d_forward(self, x):
        if x.device.type == "mps" and self.out_channels > mps_max_channels and self.groups == 1:
            w, b = self.weight, self.bias
            parts = []
            for i in range(0, self.out_channels, mps_max_channels):
                parts.append(self._conv_forward(x, w[i:i + mps_max_channels], None if b is None else b[i:i + mps_max_channels]))
            return torch.cat(parts, dim=1)
        return conv1d_forward(self, x)

    torch.nn.Conv1d.forward = chunked_conv1d_forward

    device, fallback = pick_device(torch, a.device)
    t0 = time.perf_counter()
    from resemble_enhance.enhancer.enhancer import Enhancer
    from resemble_enhance.enhancer.hparams import HParams
    from resemble_enhance.inference import inference
    from pathlib import Path

    run_dir = Path(a.model_dir) / "enhancer_stage2"
    hp = HParams.load(run_dir)
    enhancer = Enhancer(hp)
    # The checkpoint is a DeepSpeed state file; MetaRush verified its SHA-256
    # against the pinned release before this runs, so it is the published file.
    state = torch.load(run_dir / "ds" / "G" / "default" / "mp_rank_00_model_states.pt", map_location="cpu", weights_only=False)
    enhancer.load_state_dict(state["module"])
    enhancer.eval()
    try:
        enhancer.to(device)
    except Exception as e:  # the card can't hold the model: the CPU can
        if device == "cpu":
            raise
        fallback = "the model did not load on the graphics card (%s)" % e
        device = "cpu"
        enhancer.to(device)
    enhancer.configurate_(nfe=a.nfe, solver=a.solver, lambd=a.lambd, tau=a.tau)
    loaded = time.perf_counter() - t0
    ready = dict(device=device, torch=torch.__version__, load_seconds=round(loaded, 2))
    if device == "cuda":
        ready["gpu"] = torch.cuda.get_device_name(0)
    if fallback:
        ready["fallback"] = fallback
    say("ready", **ready)
    if a.check:
        say("done", index=0, device=device, torch=torch.__version__, seconds=0, audio_seconds=0, **({"fallback": fallback} if fallback else {}))
        return
    if a.jobs:
        with open(a.jobs, encoding="utf-8") as f:
            jobs = json.load(f)
    elif a.inp and a.out:
        jobs = [{"in": a.inp, "out": a.out, "denoised": a.denoised}]
    else:
        print("--in and --out (or --jobs) are required", file=sys.stderr)
        sys.exit(2)
    def run(job, mono, sr):
        with torch.inference_mode():
            if job.get("denoised"):
                dwav, dsr = inference(model=enhancer.denoiser, dwav=mono, sr=sr, device=device)
                sf.write(job["denoised"], dwav.cpu().numpy().astype("float32"), dsr, subtype="FLOAT")
            model = enhancer.denoiser if a.denoise_only else enhancer
            return inference(model=model, dwav=mono, sr=sr, device=device)

    for i, job in enumerate(jobs):
        wav, sr = sf.read(job["in"], dtype="float32", always_2d=True)
        mono = torch.from_numpy(np.ascontiguousarray(wav.mean(axis=1)))
        t1 = time.perf_counter()
        try:
            hwav, hsr = run(job, mono, sr)
        except Exception as e:
            if device == "cpu" or not out_of_memory(e):
                raise
            # Out of the card's memory: this job and the rest run on the CPU.
            fallback = "the graphics card ran out of memory (%s)" % e
            if device == "cuda":
                torch.cuda.empty_cache()
            device = "cpu"
            enhancer.to(device)
            t1 = time.perf_counter()
            hwav, hsr = run(job, mono, sr)
        took = time.perf_counter() - t1
        sf.write(job["out"], hwav.cpu().numpy().astype("float32"), hsr, subtype="FLOAT")
        done = dict(index=i, device=device, torch=torch.__version__, seconds=round(took, 2), audio_seconds=round(len(mono) / sr, 2), rate=hsr)
        if fallback:
            done["fallback"] = fallback
        say("done", **done)


if __name__ == "__main__":
    main()
