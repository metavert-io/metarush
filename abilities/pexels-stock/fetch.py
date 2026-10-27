"""Stock videos and photos from Pexels, brought in with their licence and credit.

Runs in MetaRush's sandbox: the network goes only through MetaRush, which puts the
Pexels key in on requests to api.pexels.com. This code only ever sees a stand-in
($PEXELS_API_KEY) and sends it in the Authorization header, as Pexels asks.
"""
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.pexels.com"
LICENCE = "Pexels License"
MAX_FILE = 400 << 20  # a stock clip larger than this is left out
# Where the files may come from (the hosts the manifest declares; MetaRush's
# proxy refuses any other).
MEDIA_HOSTS = {"images.pexels.com", "videos.pexels.com"}
HERE = os.path.dirname(os.path.abspath(__file__))


def inputs():
    with open(os.environ["METARUSH_INPUTS"]) as f:
        d = json.load(f)
    return {
        "words": str(d.get("words", "")).strip(),
        "kind": d.get("kind", "videos"),
        "count": int(d.get("count", 3)),
        "orientation": d.get("orientation", "any"),
    }


def ask(path, params):
    url = API + path + "?" + urllib.parse.urlencode(params)
    req = urllib.request.Request(url, headers={"Authorization": os.environ["PEXELS_API_KEY"], "User-Agent": "MetaRush pexels-stock ability"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        said = e.read().decode("utf-8", "replace")[:300]
        if e.code in (401, 403):
            sys.exit("Pexels didn't accept the key (%d). An owner sets it again: metarush ability setup pexels-stock --key pexels. %s" % (e.code, said))
        if e.code == 429:
            sys.exit("Pexels says too many requests this hour; try again later.")
        sys.exit("Pexels answered %d: %s" % (e.code, said))


def best_video_file(video):
    """The MP4 closest to 1920 wide without going over (else the smallest)."""
    files = [f for f in video.get("video_files") or [] if f.get("link") and (f.get("file_type") or "").endswith("mp4")
             and urllib.parse.urlsplit(f["link"]).hostname in MEDIA_HOSTS]
    if not files:
        return None
    fit = [f for f in files if (f.get("width") or 0) <= 1920]
    if fit:
        return max(fit, key=lambda f: (f.get("width") or 0, f.get("fps") or 0))
    return min(files, key=lambda f: f.get("width") or 0)


def picks(answer, kind, count):
    """What to bring in from a search answer: (url, file name, result entry)."""
    out = []
    if kind == "videos":
        for v in answer.get("videos") or []:
            f = best_video_file(v)
            if not f:
                continue
            who = (v.get("user") or {}).get("name") or "a Pexels contributor"
            name = "pexels-video-%s.mp4" % v.get("id")
            out.append((f["link"], name, {"path": name, "title": "Pexels video %s" % v.get("id"), "licence": LICENCE,
                                          "source": v.get("url") or "", "author": who, "credit": "Video by %s on Pexels" % who}))
    else:
        for p in answer.get("photos") or []:
            src = p.get("src") or {}
            link = src.get("large2x") or src.get("original")
            if not link or urllib.parse.urlsplit(link).hostname not in MEDIA_HOSTS:
                continue
            who = p.get("photographer") or "a Pexels contributor"
            name = "pexels-photo-%s.jpg" % p.get("id")
            title = (p.get("alt") or "").strip() or "Pexels photo %s" % p.get("id")
            out.append((link, name, {"path": name, "title": title[:120], "licence": LICENCE, "source": p.get("url") or "",
                                     "author": who, "credit": "Photo by %s on Pexels" % who}))
    return out[:count]


def fetch(url, dest):
    req = urllib.request.Request(url, headers={"User-Agent": "MetaRush pexels-stock ability"})
    with urllib.request.urlopen(req, timeout=300) as r, open(dest, "wb") as f:
        size = 0
        while True:
            block = r.read(1 << 20)
            if not block:
                break
            size += len(block)
            if size > MAX_FILE:
                raise ValueError("larger than %d MB" % (MAX_FILE >> 20))
            f.write(block)


def main():
    i = inputs()
    if not i["words"]:
        sys.exit("Say what the footage or photo shows.")
    params = {"query": i["words"], "per_page": min(15, i["count"] * 2)}
    if i["orientation"] != "any":
        params["orientation"] = i["orientation"]
    path = "/v1/videos/search" if i["kind"] == "videos" else "/v1/search"
    if os.environ.get("METARUSH_TEST"):
        # The check runs with the network off: read a saved answer instead,
        # so what the script picks is checked without reaching Pexels.
        with open(os.path.join(HERE, "test", "videos.json" if i["kind"] == "videos" else "photos.json")) as f:
            chosen = picks(json.load(f), i["kind"], i["count"])
        for url, name, entry in chosen:
            print("would bring in", name, "from", urllib.parse.urlsplit(url).hostname, "-", entry["credit"])
        if not chosen:
            sys.exit("the saved answer gave nothing to bring in")
        return
    chosen = picks(ask(path, params), i["kind"], i["count"])
    if not chosen:
        print("Pexels found nothing for “%s”." % i["words"])
    out = os.environ["METARUSH_OUTPUT"]
    kept = []
    for url, name, entry in chosen:
        try:
            fetch(url, os.path.join(out, name))
        except Exception as e:  # one file failing leaves the others
            print("left out %s: %s" % (name, e))
            try:
                os.remove(os.path.join(out, name))
            except OSError:
                pass
            continue
        kept.append(entry)
        print("brought in", name, "-", entry["credit"])
    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump({"files": kept}, f)
    if chosen and not kept:
        sys.exit("None of the files could be fetched.")


if __name__ == "__main__":
    main()
