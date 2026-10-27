"""Content Credentials (C2PA) on a delivered video with c2patool — NOT YET RUNNABLE.

The shape this ability needs, kept ready for when MetaRush has it (notes.md):
- $METARUSH_INPUTS gives the delivered video's path and its delivery record (the .json
  next to it: the version, the footage it plays, its AI-generated parts);
- c2patool adds a manifest saying what made the video and signs it through
  --signer-path, whose helper (signer.py) asks MetaRush to sign: the private key stays
  in this computer's keychain and never enters the sandbox;
- the signed copy goes to $METARUSH_OUTPUT, and MetaRush writes it next to the
  delivered file (a person starts each run).
"""
import json
import os
import subprocess
import sys


IPTC = "http://cv.iptc.org/newscodes/digitalsourcetype/"


def manifest(video, record):
    """The C2PA manifest: what made the video and which parts are AI-generated.

    A delivered video is an edit of captured footage, so its source type is
    compositeCapture ("digitalCapture" is only for footage straight from a camera);
    with AI-generated parts it is compositeWithTrainedAlgorithmicMedia. Check these
    against the C2PA and IPTC lists of the day when this becomes runnable.
    """
    generated = (record or {}).get("generated")
    source = "compositeWithTrainedAlgorithmicMedia" if generated else "compositeCapture"
    actions = [{"action": "c2pa.edited", "softwareAgent": "MetaRush", "digitalSourceType": IPTC + source}]
    return {
        "claim_generator_info": [{"name": "MetaRush"}],
        "title": (record or {}).get("title") or os.path.basename(video),
        "assertions": [{"label": "c2pa.actions", "data": {"actions": actions}}],
    }


def main():
    with open(os.environ["METARUSH_INPUTS"]) as f:
        inputs = json.load(f)
    video = inputs.get("video")
    if not video:
        sys.exit("Choose a delivered video.")
    record = None
    side = os.path.splitext(video)[0] + ".json"
    if os.path.exists(side):
        with open(side) as f:
            record = json.load(f)
    tmp = os.environ.get("TMPDIR") or "/tmp"
    m = os.path.join(tmp, "manifest.json")
    with open(m, "w") as f:
        json.dump(manifest(video, record), f)
    here = os.path.dirname(os.path.abspath(__file__))
    out = os.path.join(os.environ["METARUSH_OUTPUT"], os.path.splitext(os.path.basename(video))[0] + ".c2pa.mp4")
    r = subprocess.run(["c2patool", video, "-m", m, "-o", out, "--signer-path", "python3 " + os.path.join(here, "signer.py")],
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    print(r.stdout[-4000:])
    if r.returncode != 0:
        sys.exit("c2patool stopped with an error (status %d)." % r.returncode)


if __name__ == "__main__":
    main()
