"""Pictures from the ComfyUI server on this computer (ComfyUI's HTTP API).

ComfyUI is GPL software a person installs and runs beside MetaRush; nothing of it is
in this folder or in MetaRush. MetaRush lets this script reach exactly one service on
this computer, the one its manifest names ($METARUSH_LOCAL_URL, http://127.0.0.1:8188; an
owner agreed to it when approving), through the run's proxy; nothing else on this
computer and nothing outside it.
"""
import json
import os
import random
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

# MetaRush gives the address of the one service the manifest names (local:).
COMFY = os.environ.get("METARUSH_LOCAL_URL", "http://127.0.0.1:8188").rstrip("/")
SIZES = {"landscape": (1024, 576), "portrait": (576, 1024), "square": (768, 768)}
WAIT_SECONDS = 25 * 60


def inputs():
    with open(os.environ["METARUSH_INPUTS"]) as f:
        d = json.load(f)
    seed = d.get("seed")
    return {"words": str(d.get("words", "")).strip(), "avoid": str(d.get("avoid") or "").strip(), "model": str(d.get("model") or "").strip(),
            "shape": d.get("shape", "landscape"), "count": int(d.get("count", 1)), "steps": int(d.get("steps", 20)),
            "seed": int(seed) if seed is not None else random.randint(0, 2**32 - 1)}


def workflow(i, model):
    """ComfyUI's text-to-picture graph in its API format."""
    w, h = SIZES.get(i["shape"], SIZES["landscape"])
    return {
        "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": model}},
        "5": {"class_type": "EmptyLatentImage", "inputs": {"width": w, "height": h, "batch_size": i["count"]}},
        "6": {"class_type": "CLIPTextEncode", "inputs": {"text": i["words"], "clip": ["4", 1]}},
        "7": {"class_type": "CLIPTextEncode", "inputs": {"text": i["avoid"], "clip": ["4", 1]}},
        "3": {"class_type": "KSampler", "inputs": {"seed": i["seed"], "steps": i["steps"], "cfg": 7, "sampler_name": "euler", "scheduler": "normal",
                                                   "denoise": 1, "model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0], "latent_image": ["5", 0]}},
        "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
        "9": {"class_type": "SaveImage", "inputs": {"filename_prefix": "metarush", "images": ["8", 0]}},
    }


def call(path, body=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(COMFY + path, data=data, headers={"Content-Type": "application/json"} if data else {})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            b = r.read()
            return b if raw else json.loads(b or b"{}")
    except urllib.error.HTTPError as e:
        said = e.read().decode("utf-8", "replace")[:500]
        if e.headers.get("X-Metarush-Refused"):
            sys.exit("MetaRush refused the request: " + said.strip())
        sys.exit("ComfyUI answered %d: %s" % (e.code, said))
    except urllib.error.URLError as e:
        sys.exit("ComfyUI isn't answering at %s on this computer (%s). Start it, then run this again." % (COMFY, e.reason))


def first_model():
    info = call("/object_info/CheckpointLoaderSimple")
    names = (((info.get("CheckpointLoaderSimple") or {}).get("input") or {}).get("required") or {}).get("ckpt_name") or [[]]
    names = names[0] if names and isinstance(names[0], list) else []
    if not names:
        sys.exit("ComfyUI has no checkpoint to use: put a model in its models/checkpoints folder.")
    return names[0]


def main():
    i = inputs()
    if not i["words"]:
        sys.exit("Say what the picture shows.")
    if os.environ.get("METARUSH_TEST"):
        # The network is off in the check: build the graph and check its shape.
        g = workflow(i, "example.safetensors")
        assert g["9"]["inputs"]["images"] == ["8", 0] and g["6"]["inputs"]["text"] == i["words"]
        print("graph of %d steps for “%s”, %dx%d" % (len(g), i["words"], *SIZES[i["shape"]]))
        return
    model = i["model"] or first_model()
    answer = call("/prompt", {"prompt": workflow(i, model), "client_id": "metarush-" + uuid.uuid4().hex[:12]})
    pid = answer.get("prompt_id")
    if not pid:
        sys.exit("ComfyUI didn't take the request: %s" % json.dumps(answer.get("node_errors") or answer)[:500])
    print("ComfyUI is making it (%s, %d steps, seed %d)" % (model, i["steps"], i["seed"]))
    deadline = time.time() + WAIT_SECONDS
    history = {}
    while time.time() < deadline:
        history = (call("/history/" + urllib.parse.quote(pid)) or {}).get(pid) or {}
        status = history.get("status") or {}
        if status.get("status_str") == "error":
            sys.exit("ComfyUI stopped with an error: %s" % json.dumps(status.get("messages"))[:500])
        if history.get("outputs") and status.get("completed", True):
            break
        time.sleep(1)
    else:
        sys.exit("ComfyUI took longer than %d minutes." % (WAIT_SECONDS // 60))
    out = os.environ["METARUSH_OUTPUT"]
    kept = []
    for node in (history.get("outputs") or {}).values():
        for img in node.get("images") or []:
            if img.get("type") != "output":
                continue
            q = urllib.parse.urlencode({"filename": img.get("filename", ""), "subfolder": img.get("subfolder", ""), "type": "output"})
            name = "comfyui-%02d.png" % (len(kept) + 1)
            with open(os.path.join(out, name), "wb") as f:
                f.write(call("/view?" + q, raw=True))
            kept.append({"path": name, "title": i["words"][:100], "licence": "Made on this computer with ComfyUI (%s); the model's licence applies" % model,
                         "author": "ComfyUI on this computer"})
            print("brought in", name)
    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump({"files": kept}, f)
    if not kept:
        sys.exit("ComfyUI finished without a picture.")


if __name__ == "__main__":
    main()
