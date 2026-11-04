#!/usr/bin/env python3
"""Can MetaRush tell faces apart? (docs/visual-index.md, D73, D114)

Downloads the public-domain photos listed in faceset.json from Wikimedia
Commons into a temporary folder and fingerprints the main face of each:

  --metarush <path to metarush>  the face fingerprints MetaRush ships (D114):
      the five points (Apple Vision's landmarks on a Mac, YuNet's with the open
      models: --with apple-vision|open-models), the aligned face and SFace
      through onnxruntime, exactly as clips are fingerprinted (`metarush
      footage faces prints`).
  --helper <path to metarush-vision>  Apple Vision's general image feature
      print of the face crop (D73, measured and rejected), at several margins.

It reports how alike the same person's faces are compared with different
people's (cosine similarity), precision and recall at thresholds, nearest
neighbours, and, for --metarush, the product's own rules played out: a person
confirms a few faces of each of some people, every other face is decided as
MetaRush decides it (named automatically, asked about, or left alone), people
nobody confirmed stand in for strangers, over many random draws. The bar is the
voices' (D60): no face may be named automatically as the wrong person.
Nothing is kept or committed.

  --mix  fingerprints the photos both ways (Apple Vision's points and the open
      models') and plays the rules with faces confirmed one way and asked about
      the other, and with each face either way at random (D448): one library
      holds both where a workspace has a Mac and a computer without one.
  --suggest-sweep 0.38,0.40  plays the rules at other asking thresholds.

  python3 -I tools/vision/eval/faces.py --metarush bin/metarush [--folder photos] [--with open-models] [--suggest-sweep …]
  python3 -I tools/vision/eval/faces.py --metarush bin/metarush --folder photos --mix
  python3 -I tools/vision/eval/faces.py --helper bin/metarush-vision
"""
import argparse, base64, itertools, json, math, os, random, struct, subprocess, sys, tempfile, time, urllib.parse

HERE = os.path.dirname(os.path.abspath(__file__))
API = "https://commons.wikimedia.org/w/api.php"
UA = "metarush-face-eval/1 (https://github.com/metavert-io/metarush; a test measurement)"


def curl(url):
    return subprocess.run(["curl", "-sS", "-f", "-m", "60", "-A", UA, url], check=True, capture_output=True).stdout


def thumbs(titles, width=640):
    """The thumbnail addresses of up to 50 titles in one request (Commons rate-limits one request per photo)."""
    q = {"action": "query", "titles": "|".join(titles), "prop": "imageinfo", "iiprop": "url", "iiurlwidth": width, "format": "json"}
    d = json.loads(curl(API + "?" + urllib.parse.urlencode(q)))
    named = {n["to"]: n["from"] for n in d["query"].get("normalized", [])}
    out = {}
    for page in d["query"]["pages"].values():
        if page.get("imageinfo"):
            out[named.get(page["title"], page["title"])] = page["imageinfo"][0]["thumburl"]
    return out


def fetch(people, folder):
    want = [(os.path.join(folder, f"{who}-{i}.jpg"), title) for who, titles in people.items() for i, title in enumerate(titles, 1)]
    want = [(d, t) for d, t in want if not os.path.exists(d)]
    for start in range(0, len(want), 50):
        batch = want[start:start + 50]
        urls = {}
        for attempt in range(5):
            try:
                time.sleep(2 + 15 * attempt)  # be gentle with Commons
                urls = thumbs([t for _, t in batch])
                break
            except Exception as e:  # noqa: BLE001
                print(f"retry the photos' addresses: {e}", file=sys.stderr)
        for dest, title in batch:
            if title not in urls:
                print(f"no address for {title}", file=sys.stderr)
                continue
            for attempt in range(4):
                try:
                    time.sleep(3 + 20 * attempt)
                    data = curl(urls[title])
                    if data[:3] != b"\xff\xd8\xff":
                        raise RuntimeError("not a JPEG")
                    open(dest, "wb").write(data)
                    break
                except Exception as e:  # noqa: BLE001
                    print(f"retry {dest}: {e}", file=sys.stderr)


def unit(raw):
    v = struct.unpack("<%df" % (len(raw) // 4), raw)
    n = math.sqrt(sum(x * x for x in v)) or 1
    return [x / n for x in v]


def main_faces(lines):
    """The main (biggest) face with a fingerprint of each picture."""
    faces = {}
    for line in lines:
        d = json.loads(line)
        fs = [f for f in d.get("faces") or [] if f.get("print")]
        if not fs:
            continue
        f = max(fs, key=lambda f: f["box"][2] * f["box"][3])
        faces[d["file"]] = unit(base64.b64decode(f["print"]))
    return faces


def helper_prints(helper, folder, margin):
    out = subprocess.run([helper, "analyze", "--dir", folder, "--face-prints", "--face-margin", str(margin), "--text", "off"],
                         capture_output=True, check=True).stdout.decode()
    return main_faces(out.splitlines())


def metarush_prints(metarush, folder, home, with_=None):
    cmd = [metarush] + (["--home", home] if home else []) + ["footage", "faces", "prints", folder] + (["--with", with_] if with_ else [])
    out = subprocess.run(cmd, capture_output=True, check=True).stdout.decode()
    return main_faces(out.splitlines())


def thresholds(metarush, home):
    cmd = [metarush] + (["--home", home] if home else []) + ["footage", "faces", "prints", "--thresholds", "."]
    return json.loads(subprocess.run(cmd, capture_output=True, check=True).stdout)


def cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def dedupe(faces, alike=0.93):
    """Leaves out a person's near-copies (the same photo cropped twice), which would flatter recall."""
    keep = {}
    for name in sorted(faces, key=lambda n: (who(n), int(n.rsplit("-", 1)[1].split(".")[0]))):
        if any(who(k) == who(name) and cos(faces[k], faces[name]) >= alike for k in keep):
            continue
        keep[name] = faces[name]
    return keep


def who(name):
    return name.split("-")[0]


def pairs_report(faces):
    same, diff = [], []
    for (a, va), (b, vb) in itertools.combinations(sorted(faces.items()), 2):
        (same if who(a) == who(b) else diff).append(cos(va, vb))
    nn = sum(1 for a, va in faces.items() if who(max(((cos(va, vb), b) for b, vb in faces.items() if b != a))[1]) == who(a))
    ths = []
    for t in [x / 100 for x in range(30, 95, 5)]:
        tp, fp = sum(s >= t for s in same), sum(s >= t for s in diff)
        ths.append({"at": t, "recall": round(tp / len(same), 3), "precision": round(tp / (tp + fp), 3) if tp + fp else None})
    q = lambda xs, p: sorted(xs)[min(len(xs) - 1, int(p * len(xs)))]
    return {"faces": len(faces), "people": len({who(n) for n in faces}), "samePairs": len(same), "differentPairs": len(diff),
            "sameMin": round(min(same), 3), "sameMedian": round(q(same, 0.5), 3), "differentMedian": round(q(diff, 0.5), 3),
            "differentP99": round(q(diff, 0.99), 3), "differentMax": round(max(diff), 3), "sameAboveDifferentMax": sum(s > max(diff) for s in same),
            "nearestNeighbour": f"{nn}/{len(faces)}", "thresholds": ths}


# --- MetaRush's rules (internal/faceid/match.go), played out ---------------

def profile(vecs):
    mean = [sum(xs) for xs in zip(*vecs)]
    n = math.sqrt(sum(x * x for x in mean)) or 1
    return [x / n for x in mean], vecs


def score(v, prof):
    centroid, ex = prof
    sims = sorted((cos(v, e) for e in ex), reverse=True)[:3]
    return max(cos(v, centroid), sum(sims) / len(sims))


def decide(v, profiles, th, truth=None, worst=None, by=None):
    ranked = sorted(((score(v, p), pid) for pid, p in profiles.items()), reverse=True)
    if not ranked:
        return None, None
    if worst is not None:  # the best score anyone else got: how close a wrong name came
        wrong = [s for s, pid in ranked if pid != truth]
        if wrong:
            worst.append(wrong[0])
    need = th["auto"]
    if len(profiles) - 1 < th["fewOthers"]:
        need = max(need, th["fewAuto"])
    best, pid = ranked[0]
    second = ranked[1][0] if len(ranked) > 1 else -1
    if best >= need and best - second >= th["margin"]:
        return "auto", pid
    # A face the open models found is asked about from suggestOpen (D448).
    ask = max(th["suggest"], th.get("suggestOpen", 0)) if by == "open-models" else th["suggest"]
    if best >= ask:
        return "ask", pid
    return None, None


def play(faces, th, enroll, strangers, trials, seed, query=None, mix=None, by=None, query_by=None, mix_by=None):
    """Random draws: some people are known (enroll faces each confirmed), the rest strangers.
    query: the fingerprints faces are asked about with (default: faces, those confirmed);
    mix: other fingerprints of the same photos, each face's taken from faces or mix at random in every draw;
    by, query_by, mix_by: what found the faces of each (apple-vision or open-models)."""
    rng = random.Random(seed)
    if query is None:
        query, query_by = faces, by
    people = sorted({who(n) for n in faces})
    by = {p: sorted(n for n in faces if who(n) == p) for p in people}
    eligible = [p for p in people if len(by[p]) > enroll]
    t = {"autoRight": 0, "autoWrong": 0, "askRight": 0, "askWrong": 0, "nothing": 0, "known": 0,
         "strangerAuto": 0, "strangerAsked": 0, "strangerNothing": 0, "strangers": 0, "worstWrong": []}
    wrong_scores = []
    for _ in range(trials):
        pick = {n: ((faces[n], by) if rng.random() < 0.5 else (mix[n], mix_by)) for n in faces} if mix else None
        unknown = set(rng.sample(eligible, max(0, round(strangers * len(eligible)))))
        profiles, queries = {}, []
        for p in people:
            names = by[p][:]
            rng.shuffle(names)
            if p in unknown or p not in eligible:
                queries += [(n, None) for n in names]
                continue
            profiles[p] = profile([pick[n][0] if pick else faces[n] for n in names[:enroll]])
            queries += [(n, p) for n in names[enroll:]]
        for n, truth in queries:
            v, vby = pick[n] if pick else (query[n], query_by)
            what, pid = decide(v, profiles, th, truth, wrong_scores, vby)
            if truth is None:
                t["strangers"] += 1
                t["strangerAuto" if what == "auto" else "strangerAsked" if what == "ask" else "strangerNothing"] += 1
                if what == "auto":
                    t["worstWrong"].append(f"{n} named {pid}")
                continue
            t["known"] += 1
            if what == "auto":
                t["autoRight" if pid == truth else "autoWrong"] += 1
                if pid != truth:
                    t["worstWrong"].append(f"{n} named {pid}")
            elif what == "ask":
                t["askRight" if pid == truth else "askWrong"] += 1
            else:
                t["nothing"] += 1
    autos = t["autoRight"] + t["autoWrong"] + t["strangerAuto"]
    t["autoPrecision"] = round(t["autoRight"] / autos, 4) if autos else None
    t["autoRecall"] = round(t["autoRight"] / t["known"], 4) if t["known"] else None
    t["namedOrAskedRight"] = round((t["autoRight"] + t["askRight"]) / t["known"], 4) if t["known"] else None
    t["askedWrongPerson"] = round(t["askWrong"] / t["known"], 4) if t["known"] else None
    t["strangersAsked"] = round(t["strangerAsked"] / t["strangers"], 4) if t["strangers"] else None
    t["worstWrong"] = sorted(set(t["worstWrong"]))[:10]
    ws = sorted(wrong_scores)
    if ws:
        t["wrongPersonScoreP99"] = round(ws[int(0.99 * (len(ws) - 1))], 3)
        t["wrongPersonScoreMax"] = round(ws[-1], 3)
    return t


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--metarush", help="path to metarush (measures what ships)")
    ap.add_argument("--home", help="the MetaRush home to run it with (a test home)")
    ap.add_argument("--helper", help="path to metarush-vision (Apple's feature print)")
    ap.add_argument("--with", dest="with_", choices=["apple-vision", "open-models"], help="what finds the faces and their points (default: the computer's own way)")
    ap.add_argument("--folder", help="reuse downloaded photos")
    ap.add_argument("--trials", type=int, default=200)
    ap.add_argument("--auto", type=float, help="try another automatic-name threshold (default: what MetaRush uses)")
    ap.add_argument("--margin", type=float, help="try another lead over the runner-up")
    ap.add_argument("--suggest", type=float, help="try another asking threshold")
    ap.add_argument("--suggest-sweep", help="asking thresholds to play the rules at, comma-separated")
    ap.add_argument("--mix", action="store_true", help="fingerprint both ways and confirm one way, ask the other (D448)")
    args = ap.parse_args()
    if not args.metarush and not args.helper:
        ap.error("give --metarush or --helper")
    people = json.load(open(os.path.join(HERE, "faceset.json")))["people"]
    folder = args.folder or tempfile.mkdtemp(prefix="metarush-face-eval-")
    fetch(people, folder)
    if args.helper:
        for margin in (0.8, 1.0, 1.35, 2.0):
            print(f"Apple feature print, crop {margin}x the face: {json.dumps(pairs_report(helper_prints(args.helper, folder, margin)))}")
    if args.metarush and args.mix:
        mixed(args, folder)
        return
    if args.metarush:
        found = metarush_prints(args.metarush, folder, args.home, args.with_)
        faces = dedupe(found)
        th = thresholds(args.metarush, args.home)
        for k in ("auto", "margin", "suggest"):
            if getattr(args, k) is not None:
                th[k] = getattr(args, k)
        print(f"fingerprinted {len(found)} faces; left out {len(found) - len(faces)} near-copies of another photo of the same person")
        print(f"SFace (what ships{', points from ' + args.with_ if args.with_ else ''}): {json.dumps(pairs_report(faces))}")
        print(f"thresholds: {json.dumps(th)}")
        for enroll in (1, 2, 3):
            r = play(faces, th, enroll, 0.25, args.trials, 7 + enroll, by=args.with_)
            print(f"confirmed {enroll} face(s) per known person, a quarter of people unknown, {args.trials} draws: {json.dumps(r)}")
        people = len({who(n) for n in faces})
        for known in (1, 2):
            r = play(faces, th, 2, 1 - known / people, args.trials, 31 + known, by=args.with_)
            print(f"only {known} known (2 faces confirmed each), everyone else a stranger, {args.trials} draws: {json.dumps(r)}")
        for at in [float(x) for x in (args.suggest_sweep or "").split(",") if x]:
            t = dict(th, suggest=at, suggestOpen=at)
            for enroll in (1, 2, 3):
                r = play(faces, t, enroll, 0.25, args.trials, 7 + enroll, by=args.with_)
                print(f"asking from {at}: confirmed {enroll}: askedWrongPerson {r['askedWrongPerson']} namedOrAskedRight {r['namedOrAskedRight']} strangersAsked {r['strangersAsked']} autoPrecision {r['autoPrecision']}")


def summary(r):
    keep = ("autoPrecision", "autoRecall", "namedOrAskedRight", "askedWrongPerson", "strangersAsked", "autoWrong", "strangerAuto", "wrongPersonScoreMax")
    return {k: r.get(k) for k in keep}


def mixed(args, folder):
    """Both ways in one library (D448): confirmed with one, asked about with the other, or each face either way."""
    A, O = dedupe(metarush_prints(args.metarush, folder, args.home, "apple-vision")), dedupe(metarush_prints(args.metarush, folder, args.home, "open-models"))
    both = sorted(set(A) & set(O))
    a, o = {n: A[n] for n in both}, {n: O[n] for n in both}
    th = thresholds(args.metarush, args.home)
    print(f"photos fingerprinted both ways: {len(both)} (Apple Vision {len(A)}, the open models {len(O)})")
    same_photo = sorted(cos(a[n], o[n]) for n in both)
    print(f"the same photo, the two ways' fingerprints: min {same_photo[0]:.3f} median {same_photo[len(same_photo) // 2]:.3f}")
    same, diff = [], []
    for x, y in itertools.permutations(both, 2):
        (same if who(x) == who(y) else diff).append(cos(a[x], o[y]))
    q = lambda xs, p: sorted(xs)[min(len(xs) - 1, int(p * len(xs)))]
    print(f"across the two ways: same person median {q(same, 0.5):.3f}; different people median {q(diff, 0.5):.3f} p99 {q(diff, 0.99):.3f} max {max(diff):.3f}")
    people = len({who(n) for n in both})
    A_, O_ = "apple-vision", "open-models"
    for label, faces, by, query, query_by, mix in (("Apple Vision", a, A_, None, None, None), ("the open models", o, O_, None, None, None),
                                                   ("confirmed with Apple Vision, asked with the open models", a, A_, o, O_, None),
                                                   ("confirmed with the open models, asked with Apple Vision", o, O_, a, A_, None),
                                                   ("each face either way at random", a, A_, None, None, o)):
        kw = dict(query=query, mix=mix, by=by, query_by=query_by, mix_by=O_)
        for enroll in (1, 2, 3):
            print(f"{label}, confirmed {enroll}: {json.dumps(summary(play(faces, th, enroll, 0.25, args.trials, 7 + enroll, **kw)))}")
        for known in (1, 2):
            print(f"{label}, only {known} known: {json.dumps(summary(play(faces, th, 2, 1 - known / people, args.trials, 31 + known, **kw)))}")


if __name__ == "__main__":
    main()
