"""Sound effects from Freesound, brought in with their licence and credit.

Runs in MetaRush's sandbox, its network only through MetaRush, which puts the
Freesound key in on requests to freesound.org. This code only ever sees a stand-in
($FREESOUND_API_KEY) and sends it as Freesound asks: "Authorization: Token <key>".
A key gives the high-quality previews (MP3, about 128 kbps); the original files need
a Freesound sign-in (OAuth2), which abilities don't do.
"""
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

API = "https://freesound.org/apiv2"
MEDIA_HOSTS = {"cdn.freesound.org", "freesound.org"}
MAX_FILE = 50 << 20
HERE = os.path.dirname(os.path.abspath(__file__))
FILTERS = {"cc0-only": 'license:"Creative Commons 0"', "cc0-and-cc-by": 'license:("Creative Commons 0" OR "Attribution")'}


def inputs():
    with open(os.environ["METARUSH_INPUTS"]) as f:
        d = json.load(f)
    return {"words": str(d.get("words", "")).strip(), "count": int(d.get("count", 3)), "longest": float(d.get("longest", 30)),
            "licences": d.get("licences", "cc0-and-cc-by")}


def licence_of(url):
    """Freesound gives a licence as its address; only CC0 and CC BY come in."""
    m = re.search(r"creativecommons\.org/(licenses|publicdomain)/([a-z+-]+)/([0-9.]+)", (url or "").lower())
    if not m:
        return None
    if m.group(1) == "publicdomain" and m.group(2) == "zero":
        return "CC0 " + m.group(3)
    if m.group(1) == "licenses" and m.group(2) == "by":
        return "CC BY " + m.group(3)
    return None  # NC and Sampling+ are left out


def picks(answer, count, cc0_only):
    out = []
    for s in answer.get("results") or []:
        name = (s.get("name") or "sound %s" % s.get("id")).strip()
        licence = licence_of(s.get("license"))
        if not licence or (cc0_only and not licence.startswith("CC0")):
            print("left out “%s”: its licence (%s) isn't one asked for" % (name, s.get("license") or "none stated"))
            continue
        link = (s.get("previews") or {}).get("preview-hq-mp3")
        if not link or urllib.parse.urlsplit(link).hostname not in MEDIA_HOSTS:
            print("left out “%s”: no preview MetaRush can fetch" % name)
            continue
        title = re.sub(r"\.(wav|aiff?|flac|mp3|ogg|m4a)$", "", name, flags=re.I)
        who = s.get("username") or "a Freesound user"
        page = s.get("url") or "https://freesound.org/s/%s/" % s.get("id")
        slug = re.sub(r"[^A-Za-z0-9]+", "-", title).strip("-").lower()[:50] or "sound"
        out.append((link, "freesound-%s-%s.mp3" % (s.get("id"), slug), {
            "title": title[:120], "licence": licence, "source": page, "author": who,
            "credit": "“%s” by %s, %s, via Freesound" % (title, who, licence)}))
        if len(out) >= count:
            break
    return out


def ask(params):
    req = urllib.request.Request(API + "/search/text/?" + urllib.parse.urlencode(params),
                                 headers={"Authorization": "Token " + os.environ["FREESOUND_API_KEY"], "User-Agent": "MetaRush freesound-effects ability"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        said = e.read().decode("utf-8", "replace")[:300]
        if e.code in (401, 403):
            sys.exit("Freesound didn't accept the key (%d). An owner sets it again: metarush ability setup freesound-effects --key freesound. %s" % (e.code, said))
        if e.code == 429:
            sys.exit("Freesound says too many requests (60 a minute, 2,000 a day); try again later.")
        sys.exit("Freesound answered %d: %s" % (e.code, said))


def fetch(url, dest):
    req = urllib.request.Request(url, headers={"User-Agent": "MetaRush freesound-effects ability"})
    with urllib.request.urlopen(req, timeout=120) as r, open(dest, "wb") as f:
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
        sys.exit("Say what the sound is.")
    params = {"query": i["words"], "filter": "%s duration:[0 TO %g]" % (FILTERS.get(i["licences"], FILTERS["cc0-and-cc-by"]), i["longest"]),
              "fields": "id,name,username,license,url,previews,duration", "page_size": str(min(30, i["count"] * 3))}
    cc0_only = i["licences"] == "cc0-only"
    if os.environ.get("METARUSH_TEST"):
        # The network is off in the check: read a saved answer instead.
        with open(os.path.join(HERE, "test", "search.json")) as f:
            chosen = picks(json.load(f), i["count"], cc0_only)
        for _, name, entry in chosen:
            print("would bring in", name, "-", entry["credit"])
        if not chosen:
            sys.exit("the saved answer gave nothing to bring in")
        return
    chosen = picks(ask(params), i["count"], cc0_only)
    if not chosen:
        print("Freesound found nothing for “%s” under those licences." % i["words"])
    out = os.environ["METARUSH_OUTPUT"]
    kept = []
    for url, name, entry in chosen:
        try:
            fetch(url, os.path.join(out, name))
        except Exception as e:  # one sound failing leaves the others
            print("left out %s: %s" % (name, e))
            try:
                os.remove(os.path.join(out, name))
            except OSError:
                pass
            continue
        kept.append(dict(entry, path=name))
        print("brought in", name, "-", entry["credit"])
    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump({"files": kept}, f)
    if chosen and not kept:
        sys.exit("None of the sounds could be fetched.")


if __name__ == "__main__":
    main()
