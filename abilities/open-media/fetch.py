"""Public-domain and openly licensed media from Wikimedia Commons and the Internet Archive.

Runs in MetaRush's sandbox, its network only through MetaRush (no key). Only files
whose licence lets you use and change them in a video are brought in: public domain,
CC0, CC BY and CC BY-SA. Anything "non-commercial" or "no derivatives", unclear or
unstated is left out, with the reason. Each file says its licence, its page, its
author and the credit line its licence asks for (title, author, licence, source).
"""
import html.parser
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

COMMONS_API = "https://commons.wikimedia.org/w/api.php"
ARCHIVE = "https://archive.org"
MAX_FILE = 500 << 20
HERE = os.path.dirname(os.path.abspath(__file__))
TEST = bool(os.environ.get("METARUSH_TEST"))
SAVED = {}  # the check's saved answers, by what was asked (the network is off then)


def inputs():
    with open(os.environ["METARUSH_INPUTS"]) as f:
        d = json.load(f)
    return {"words": str(d.get("words", "")).strip(), "kind": d.get("kind", "pictures"), "from": d.get("from", "both"),
            "count": int(d.get("count", 3)), "contact": str(d.get("contact") or "").strip()}


def agent(contact):
    # Wikimedia asks every tool to say who it is and how to reach its user.
    return "MetaRush-open-media/1.0 (%s) Python-urllib/%d.%d" % (contact or "a MetaRush ability; no contact given", sys.version_info[0], sys.version_info[1])


def get_json(url, ua, saved):
    if TEST:
        with open(os.path.join(HERE, "test", saved)) as f:
            return json.load(f)
    req = urllib.request.Request(url, headers={"User-Agent": ua, "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


class _Text(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []

    def handle_data(self, data):
        self.parts.append(data)


def text_of(s):
    """Wikimedia's metadata is HTML: its words, on one line."""
    p = _Text()
    p.feed(s or "")
    return " ".join(" ".join(p.parts).split())


# ---- licences ----

def licence_from_url(url):
    """A Creative Commons or public-domain address → its short name, or None
    when it doesn't let you use and change the file in a video."""
    u = (url or "").lower().replace("http://", "https://")
    m = re.search(r"creativecommons\.org/(licenses|publicdomain)/([a-z+-]+)/([0-9.]+)?", u)
    if not m:
        return None
    kind, what, ver = m.group(1), m.group(2), m.group(3) or ""
    if kind == "publicdomain":
        return {"zero": "CC0 " + (ver or "1.0"), "mark": "Public Domain Mark " + (ver or "1.0")}.get(what)
    if what in ("by", "by-sa"):
        return ("CC " + what.upper() + " " + ver).strip()
    return None  # NC, ND, sampling: left out


def commons_licence(meta):
    """Wikimedia's licence fields → (short name, why it is left out)."""
    short = text_of((meta.get("LicenseShortName") or {}).get("value"))
    code = text_of((meta.get("License") or {}).get("value")).lower()
    url = text_of((meta.get("LicenseUrl") or {}).get("value"))
    low = short.lower()
    if not short and not code:
        return None, "it states no licence"
    if re.search(r"\bnc\b|non-?commercial|\bnd\b|no ?deriv|fair use|non-?free", low + " " + code):
        return None, "its licence (%s) doesn't let you use and change it in any video" % short
    if code in ("pd", "cc0") or low.startswith("public domain") or low.startswith("pd") or low.startswith("cc0") or "cc-zero" in low:
        return (short if short else "Public domain"), None
    if re.match(r"^cc[ -]by(-sa)?([ -][0-9.]+)?", low) or code.startswith("cc-by"):
        return short or licence_from_url(url) or code.upper(), None
    return None, "its licence (%s) isn't one this ability brings in (public domain, CC0, CC BY, CC BY-SA)" % short


def credit_line(title, author, licence, where):
    return "“%s” by %s, %s, via %s" % (title, author or "an unknown author", licence, where)


# ---- Wikimedia Commons ----

FILETYPE = {"pictures": "bitmap", "video": "video", "sound": "audio"}
AUDIO_EXT = {"ogg", "oga", "opus", "mp3", "flac", "wav"}


def commons(words, kind, count, ua):
    search = "%s filetype:%s" % (words, FILETYPE[kind])
    prop = "videoinfo" if kind == "video" else "imageinfo"
    pre = "vi" if kind == "video" else "ii"
    q = {"action": "query", "format": "json", "formatversion": "2", "generator": "search", "gsrsearch": search, "gsrnamespace": "6",
         "gsrlimit": str(min(50, count * 4)), "prop": prop, pre + "prop": "url|size|mime|extmetadata" + ("|derivatives" if kind == "video" else ""),
         pre + "extmetadatalanguage": "en"}
    if kind == "pictures":
        q["iiurlwidth"] = "1920"
    answer = get_json(COMMONS_API + "?" + urllib.parse.urlencode(q), ua, "commons-%s.json" % kind)
    pages = (answer.get("query") or {}).get("pages") or []
    if isinstance(pages, dict):
        pages = list(pages.values())
    pages.sort(key=lambda p: p.get("index", 0))
    out = []
    for p in pages:
        info = (p.get("videoinfo") or p.get("imageinfo") or [{}])[0]
        meta = info.get("extmetadata") or {}
        name = p.get("title", "").split(":", 1)[-1]
        title = os.path.splitext(name)[0].replace("_", " ")
        licence, why = commons_licence(meta)
        if not licence:
            print("left out “%s”: %s" % (title, why))
            continue
        link, ext = pick_commons_file(kind, info)
        if not link:
            print("left out “%s”: no file MetaRush takes in" % title)
            continue
        author = text_of((meta.get("Artist") or {}).get("value"))[:120]
        restrictions = text_of((meta.get("Restrictions") or {}).get("value"))
        if restrictions:
            print("note on “%s”: %s (for example trademarks or a person's likeness)" % (title, restrictions))
        page = info.get("descriptionurl") or "https://commons.wikimedia.org/wiki/" + urllib.parse.quote(p.get("title", "").replace(" ", "_"))
        out.append({"url": link, "ext": ext, "size": info.get("size") or 0, "entry": {
            "title": title[:120], "licence": licence, "source": page, "author": author,
            "credit": credit_line(title, author, licence, "Wikimedia Commons")}})
        if len(out) >= count:
            break
    return out


def pick_commons_file(kind, info):
    mime = (info.get("mime") or "").lower()
    if kind == "pictures":
        if mime in ("image/jpeg", "image/png", "image/webp") and (info.get("width") or 0) <= 1920:
            return info.get("url"), ext_of(info.get("url"))
        return info.get("thumburl"), ext_of(info.get("thumburl"))  # a scaled JPEG or PNG Wikimedia makes
    if kind == "video":
        if mime == "video/webm" and (info.get("size") or 0) <= MAX_FILE and (info.get("height") or 0) <= 1080:
            return info.get("url"), "webm"
        best = None
        for d in info.get("derivatives") or []:
            if "webm" in (d.get("type") or "") and (d.get("height") or 0) <= 1080:
                if best is None or (d.get("height") or 0) > (best.get("height") or 0):
                    best = d
        return (best.get("src"), "webm") if best else (None, None)
    ext = ext_of(info.get("url"))
    if ext in AUDIO_EXT:
        return info.get("url"), "ogg" if ext == "oga" else ext  # .oga is Ogg sound
    return None, None


def ext_of(url):
    path = urllib.parse.urlsplit(url or "").path
    return os.path.splitext(path)[1].lstrip(".").lower()


# ---- the Internet Archive ----

MEDIATYPE = {"pictures": "image", "video": "movies", "sound": "audio"}
WANT = {"pictures": ("jpg", "jpeg", "png"), "video": ("mp4",), "sound": ("mp3", "ogg", "flac", "wav")}


def archive(words, kind, count, ua):
    q = "(%s) AND mediatype:(%s) AND (licenseurl:*creativecommons.org* OR licenseurl:*publicdomain*)" % (words.replace('"', ""), MEDIATYPE[kind])
    params = [("q", q), ("rows", str(min(50, count * 3))), ("output", "json"), ("sort[]", "downloads desc")]
    params += [("fl[]", f) for f in ("identifier", "title", "creator", "licenseurl", "mediatype")]
    answer = get_json(ARCHIVE + "/advancedsearch.php?" + urllib.parse.urlencode(params), ua, "archive-%s.json" % kind)
    out = []
    for doc in (answer.get("response") or {}).get("docs") or []:
        ident = doc.get("identifier") or ""
        if not re.match(r"^[A-Za-z0-9._-]+$", ident):
            continue
        title = first(doc.get("title")) or ident
        licence = licence_from_url(first(doc.get("licenseurl")))
        if not licence:
            print("left out “%s”: its licence (%s) doesn't let you use and change it in any video" % (title, first(doc.get("licenseurl")) or "none stated"))
            continue
        try:
            meta = get_json(ARCHIVE + "/metadata/" + ident, ua, "archive-%s-%s.json" % (kind, ident))
        except (urllib.error.URLError, ValueError, OSError) as e:
            # One item the Internet Archive can't describe now is left out, not the run.
            print("left out “%s”: the Internet Archive didn't describe it (%s)" % (title, getattr(e, "code", None) or e))
            continue
        f = pick_archive_file(kind, meta.get("files") or [])
        if not f:
            print("left out “%s”: no file MetaRush takes in" % title)
            continue
        author = first(doc.get("creator")) or first((meta.get("metadata") or {}).get("creator"))
        name = f["name"]
        out.append({"url": "%s/download/%s/%s" % (ARCHIVE, ident, urllib.parse.quote(name)), "ext": ext_of(name), "size": int(f.get("size") or 0), "entry": {
            "title": title[:120], "licence": licence, "source": "%s/details/%s" % (ARCHIVE, ident), "author": (author or "")[:120],
            "credit": credit_line(title, author, licence, "the Internet Archive")}})
        if len(out) >= count:
            break
    return out


def first(v):
    if isinstance(v, list):
        return str(v[0]).strip() if v else ""
    return str(v or "").strip()


def pick_archive_file(kind, files):
    ok = []
    for f in files:
        name = f.get("name") or ""
        fmt = (f.get("format") or "").lower()
        size = int(f.get("size") or 0)
        if "/" in name or name.startswith("__ia_thumb") or "thumb" in fmt or "tile" in fmt or size > MAX_FILE:
            continue
        if ext_of(name) in WANT[kind]:
            ok.append(f)
    if not ok:
        return None
    # The original when there is one, else the largest copy that fits.
    ok.sort(key=lambda f: (f.get("source") == "original", int(f.get("size") or 0)), reverse=True)
    return ok[0]


# ---- bringing them in ----

def fetch(url, dest, ua):
    req = urllib.request.Request(url, headers={"User-Agent": ua})
    with urllib.request.urlopen(req, timeout=600) as r, open(dest, "wb") as f:
        size = 0
        while True:
            block = r.read(1 << 20)
            if not block:
                break
            size += len(block)
            if size > MAX_FILE:
                raise ValueError("larger than %d MB" % (MAX_FILE >> 20))
            f.write(block)


def safe_name(n, title, ext):
    base = re.sub(r"[^A-Za-z0-9]+", "-", title).strip("-")[:60] or "file"
    return "%02d-%s.%s" % (n, base.lower(), ext)


def main():
    i = inputs()
    if not i["words"]:
        sys.exit("Say what it shows or sounds like.")
    ua = agent(i["contact"])
    found, failed = [], []
    sources = []
    if i["from"] in ("both", "wikimedia-commons"):
        sources.append(("Wikimedia Commons", commons))
    if i["from"] in ("both", "internet-archive"):
        sources.append(("the Internet Archive", archive))
    for where, search in sources:
        try:
            found.append(search(i["words"], i["kind"], i["count"], ua))
        except (urllib.error.URLError, ValueError, OSError) as e:
            # One place not answering leaves the other's files.
            print("%s didn't answer the search (%s); carrying on without it." % (where, getattr(e, "code", None) or e))
            failed.append(where)
    if failed and len(failed) == len(sources):
        sys.exit("Neither place answered; try again later." if len(sources) > 1 else "%s didn't answer; try again later." % failed[0])
    # Take turns between the two, up to the count.
    chosen = []
    while len(chosen) < i["count"] and any(found):
        for lst in found:
            if lst and len(chosen) < i["count"]:
                chosen.append(lst.pop(0))
    if TEST:
        for c in chosen:
            print("would bring in", c["entry"]["title"], "-", c["entry"]["credit"])
        if not chosen:
            sys.exit("the saved answers gave nothing to bring in")
        return
    if not chosen:
        print("Nothing openly licensed was found for “%s”." % i["words"])
    out = os.environ["METARUSH_OUTPUT"]
    kept = []
    for n, c in enumerate(chosen, 1):
        name = safe_name(n, c["entry"]["title"], c["ext"])
        try:
            fetch(c["url"], os.path.join(out, name), ua)
        except Exception as e:  # one file failing leaves the others
            print("left out %s: %s" % (name, e))
            try:
                os.remove(os.path.join(out, name))
            except OSError:
                pass
            continue
        entry = dict(c["entry"], path=name)
        kept.append(entry)
        print("brought in", name, "-", entry["credit"])
    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump({"files": kept}, f)
    if chosen and not kept:
        sys.exit("None of the files could be fetched.")


if __name__ == "__main__":
    main()
