"""Checks MetaRush's timeline exports (D92) with the official OpenTimelineIO library.

Run by scripts/interchange-check.sh inside a throwaway virtual environment that
has opentimelineio, otio-fcp-adapter (the fcp_xml adapter for Final Cut Pro 7
XML), otio-fcpx-xml-adapter (FCPXML) and otio-cmx3600-adapter (EDL), pinned in
scripts/interchange-requirements.txt. Never a MetaRush dependency.

For the fixture timeline (written by the Go tests with INTERCHANGE_OUT), every
file is read back and compared with what the Go side meant (fixture.expected.json):
  - .otio: every track, clip, gap and transition at the expected frames, source
    in points, markers; then a round trip (write, read, is_equivalent_to) and a
    write with the OpenTimelineIO 0.14 schemas.
  - .xml (xmeml): read with the fcp_xml adapter; picture and sound clips at the
    expected frames, transitions on the expected edit points, markers.
  - .fcpxml: read with the fcpx_xml adapter; the storyline's clips at the
    expected frames, the connected sounds, markers.
Both XML formats are also validated against their DTDs with xmllint (FCPXML
1.8 from Final Cut Pro's DTD, xmeml from Apple's DTD for version 5), fetched
from public copies into a temporary folder.

Extra files on the command line (a real export) get the read, round trip and
DTD checks without the expected layout.

With --other-out, OpenTimelineIO also writes the fixture in every format it
writes (OTIO, FCP7 XML, a CMX 3600 EDL of the picture; its FCPXML writer is
tried) with what plays at sampled frames, for MetaRush's own timeline readers
(D303) to read back: files another program wrote (TestReadOtherWriters).
"""
import argparse
import html.parser
import json
import os
import subprocess
import sys
import urllib.parse
import urllib.request

import opentimelineio as otio

FCPXML_DTD = "https://raw.githubusercontent.com/CommandPost/CommandPost/develop/src/extensions/cp/apple/fcpxml/dtd/FCPXMLv1_8.dtd"
XMEML_DTD_PAGE = "https://developer.apple.com/library/archive/documentation/AppleApplications/Reference/FinalCutPro_XML/DTD/DTD.html"

problems = []
checks = 0


def check(ok, what):
    global checks
    checks += 1
    if not ok:
        problems.append(what)
    return ok


def frames(t, fps):
    return round(t.rescaled_to(fps).value, 3)


class _PreBlocks(html.parser.HTMLParser):
    """The <pre> blocks under one heading of Apple's DTD page (one declaration each)."""

    def __init__(self, heading):
        super().__init__(convert_charrefs=True)
        self.want, self.cur_h, self.in_h, self.h = heading, "", False, []
        self.depth, self.cur, self.lines = 0, [], []

    def handle_starttag(self, tag, attrs):
        if tag == "pre":
            self.depth, self.cur = self.depth + 1, []
        if tag in ("h1", "h2", "h3"):
            self.in_h, self.h = True, []

    def handle_endtag(self, tag):
        if tag == "pre" and self.depth:
            self.depth -= 1
            if self.cur_h == self.want:
                self.lines.append("".join(self.cur).strip())
        if tag in ("h1", "h2", "h3") and self.in_h:
            self.in_h, self.cur_h = False, "".join(self.h).strip()

    def handle_data(self, data):
        if self.depth:
            self.cur.append(data)
        if self.in_h:
            self.h.append(data)


def fetch_dtds(folder):
    os.makedirs(folder, exist_ok=True)
    fcp = os.path.join(folder, "FCPXMLv1_8.dtd")
    xm = os.path.join(folder, "xmeml-v5.dtd")
    if not os.path.exists(fcp):
        urllib.request.urlretrieve(FCPXML_DTD, fcp)
    if not os.path.exists(xm):
        page = urllib.request.urlopen(XMEML_DTD_PAGE, timeout=30).read().decode("utf-8")
        p = _PreBlocks("DTD for Version 5 of the Interchange Format")
        p.feed(page)
        if len(p.lines) < 100:
            raise SystemExit("could not read the xmeml DTD from Apple's page")
        with open(xm, "w", encoding="utf-8") as f:
            f.write("\n".join(p.lines) + "\n")
    return fcp, xm


def dtd_valid(path, dtd):
    r = subprocess.run(["xmllint", "--noout", "--dtdvalid", dtd, path], capture_output=True, text=True)
    out = (r.stderr or r.stdout).strip()
    check(r.returncode == 0, f"{os.path.basename(path)} is not valid against {os.path.basename(dtd)}:\n{out[:2000]}")
    return r.returncode == 0


def otio_segments(track, fps):
    """The track as (kind, name, start, duration, source, before, after) in timeline frames."""
    out = []
    for i, child in enumerate(track):
        if isinstance(child, otio.schema.Transition):
            # The edit point is the next item's start.
            nxt = track.range_of_child_at_index(i + 1).start_time if i + 1 < len(track) else track.range_of_child_at_index(i - 1).end_time_exclusive()
            out.append(("transition", "", frames(nxt, fps), 0, None, frames(child.in_offset, fps), frames(child.out_offset, fps)))
            continue
        r = track.range_of_child_at_index(i)
        if isinstance(child, otio.schema.Gap):
            out.append(("gap", "", frames(r.start_time, fps), frames(r.duration, fps), None, 0, 0))
        else:
            src = frames(child.source_range.start_time, fps) if child.source_range else None
            out.append(("clip", child.name, frames(r.start_time, fps), frames(r.duration, fps), src, 0, 0))
    return out


def compare_track(fmt, got, want, fps, compare_sources=True, compare_transitions=True, speed_sources=True):
    """speed_sources=False: skip the source in point of speed-changed clips (the
    fcp_xml reader moves a clip's in point under a transition by timeline
    frames, not by the clip's speed)."""
    w = [s for s in want["segments"] if compare_transitions or s["kind"] != "transition"]
    g = [s for s in got if compare_transitions or s[0] != "transition"]
    if not check(len(g) == len(w), f"{fmt} {want['name']}: {len(g)} elements, expected {len(w)}: {g} vs {[s['kind'] for s in w]}"):
        return
    for a, b in zip(g, w):
        kind, name, start, dur, src, before, after = a
        ok = kind == b["kind"]
        if b["kind"] == "clip":
            ok = ok and name == b["name"] and abs(start - b["start"]) < 0.01 and abs(dur - b["duration"]) < 0.01
            sped = b.get("speed", 1) not in (0, 1)
            if compare_sources and src is not None and (speed_sources or not sped):
                ok = ok and abs(src - b.get("source", 0)) < 0.51
        elif b["kind"] == "gap":
            ok = ok and abs(dur - b["duration"]) < 0.01
        elif b["kind"] == "transition":
            ok = ok and abs(start - b["start"]) < 0.01 and abs(before - b.get("before", 0)) < 0.01 and abs(after - b.get("after", 0)) < 0.01
        check(ok, f"{fmt} {want['name']}: got {a}, expected {b}")


def check_otio(path, exp):
    tl = otio.adapters.read_from_file(path)
    check(isinstance(tl, otio.schema.Timeline), f"{path}: not a timeline")
    fps = exp["fps"] if exp else 30
    if exp:
        tracks = list(tl.tracks)
        check(len(tracks) == len(exp["tracks"]), f"otio: {len(tracks)} tracks, expected {len(exp['tracks'])}")
        for t, want in zip(tracks, exp["tracks"]):
            check(t.name == want["name"], f"otio: track {t.name!r}, expected {want['name']!r}")
            compare_track("otio", otio_segments(t, fps), want, fps)
        check(frames(tl.tracks[0].duration(), fps) == exp["duration"], f"otio: picture lasts {frames(tl.tracks[0].duration(), fps)}, expected {exp['duration']}")
        check(len(tl.tracks.markers) == exp["markers"], f"otio: {len(tl.tracks.markers)} markers, expected {exp['markers']}")
    # Round trip through the library, current and 0.14 schemas.
    s = otio.adapters.write_to_string(tl, "otio_json")
    back = otio.adapters.read_from_string(s, "otio_json")
    check(back.is_equivalent_to(tl), "otio: not equivalent after writing and reading back")
    old = otio.adapters.write_to_string(tl, "otio_json", target_schema_versions=otio.versioning.fetch_map("OTIO_CORE", "0.14.0"))
    check(otio.adapters.read_from_string(old, "otio_json").is_equivalent_to(tl), "otio: not equivalent through the 0.14 schemas")
    clips = list(tl.find_clips())
    print(f"  otio: {len(list(tl.tracks))} tracks, {len(clips)} clips, {len(tl.tracks.markers)} markers, {frames(tl.duration(), fps)} frames; round trip OK")
    return tl


def check_xmeml(path, exp):
    tl = otio.adapters.read_from_file(path, adapter_name="fcp_xml")
    fps = exp["fps"] if exp else 30
    if exp:
        tracks = list(tl.tracks)
        video = [t for t in tracks if t.kind == otio.schema.TrackKind.Video]
        audio = [t for t in tracks if t.kind == otio.schema.TrackKind.Audio]
        ev = [t for t in exp["tracks"] if t["kind"] == "video"]
        ea = [t for t in exp["tracks"] if t["kind"] == "audio"]
        check(len(video) == len(ev), f"xmeml: {len(video)} picture tracks, expected {len(ev)}")
        check(len(audio) == 2 * len(ea), f"xmeml: {len(audio)} sound tracks, expected {2 * len(ea)} (stereo pairs)")
        for t, want in zip(video, ev):
            compare_track("xmeml", otio_segments(t, fps), want, fps, speed_sources=False)
        for i, want in enumerate(ea):
            for ch in (0, 1):
                compare_track(f"xmeml (channel {ch + 1})", otio_segments(audio[2 * i + ch], fps), want, fps, speed_sources=False)
        check(len(tl.tracks.markers) == exp["markers"], f"xmeml: {len(tl.tracks.markers)} markers, expected {exp['markers']}")
    print(f"  xmeml: {len(list(tl.tracks))} tracks, {len(list(tl.find_clips()))} clips, {len(tl.tracks.markers)} markers (fcp_xml adapter)")


def seconds(t):
    """FCPXML time ("19/5s", "3s") in seconds."""
    t = t.rstrip("s")
    if "/" in t:
        n, d = t.split("/")
        return float(n) / float(d)
    return float(t)


def check_fcpxml(path, exp):
    got = otio.adapters.read_from_file(path, adapter_name="fcpx_xml")
    tl = got[0] if isinstance(got, otio.schema.SerializableCollection) else got
    fps = exp["fps"] if exp else 30
    tracks = list(tl.tracks)
    if exp:
        lane0 = [t for t in tracks if t.name == "0"]
        want = exp["fcpxmlPicture"]
        if check(len(lane0) == 1, f"fcpxml: no primary storyline among {[t.name for t in tracks]}"):
            clips = [c for c in lane0[0] if isinstance(c, (otio.schema.Clip, otio.schema.Stack))]
            check(len(clips) == len(want), f"fcpxml: {len(clips)} storyline clips, expected {len(want)}")
            for c, b in zip(clips, want):
                # Names and lengths from the adapter; it adds frame counts of different rates
                # for mixed-rate footage, so positions are read from the file below.
                ok = c.name == b["name"] and abs(frames(c.source_range.duration, fps) - b["duration"]) < 0.51
                check(ok, f"fcpxml storyline (adapter): got {c.name} {frames(c.source_range.duration, fps)}, expected {b}")
        import xml.etree.ElementTree as ET
        spine = ET.parse(path).getroot().find("./library/event/project/sequence/spine")
        items = [e for e in spine if e.tag in ("asset-clip", "ref-clip", "clip", "gap")]
        check(len(items) == len(want), f"fcpxml spine: {len(items)} elements, expected {len(want)}")
        for e, b in zip(items, want):
            off, dur = round(seconds(e.get("offset", "0s")) * fps, 3), round(seconds(e.get("duration")) * fps, 3)
            check(abs(off - b["start"]) < 0.01 and abs(dur - b["duration"]) < 0.01, f"fcpxml spine {e.get('name')}: offset {off} duration {dur}, expected {b}")
        connected = sum(len(list(t.find_clips())) for t in tracks if t.name != "0")
        want_conn = sum(len([s for s in t["segments"] if s["kind"] == "clip"]) for t in exp["tracks"] if t["role"] not in ("picture", "clip-sound"))
        want_conn += exp.get("fcpxmlDetached", 0)  # clip sounds from another file (clearer voices)
        check(connected == want_conn, f"fcpxml: {connected} connected clips, expected {want_conn}")
        markers = sum(len(c.markers) for c in tl.find_clips())
        check(markers == exp["markers"], f"fcpxml: {markers} markers, expected {exp['markers']}")
    print(f"  fcpxml: {len(tracks)} lanes, {len(list(tl.find_clips()))} clips (fcpx_xml adapter)")


def picture_samples(tl, fps, step=3, skip_speed=False):
    """What the first picture track plays at every step-th frame, away from
    transitions: [frame, file name, seconds into the file]. skip_speed leaves
    out clips that change speed."""
    track = next(t for t in tl.tracks if t.kind == otio.schema.TrackKind.Video)
    ranges = []
    for i, child in enumerate(track):
        ranges.append((child, track.range_of_child_at_index(i)))
    total = int(round(track.duration().rescaled_to(fps).value))
    out = []
    for f in range(0, total, step):
        t = otio.opentime.RationalTime(f, fps)
        near_transition = False
        for child, r in ranges:
            if isinstance(child, otio.schema.Transition):
                lo = r.start_time.rescaled_to(fps).value - 1
                hi = r.end_time_exclusive().rescaled_to(fps).value + 1
                if lo <= f <= hi:
                    near_transition = True
        if near_transition:
            continue
        for child, r in ranges:
            if isinstance(child, otio.schema.Transition) or not r.contains(t):
                continue
            if isinstance(child, otio.schema.Gap):
                out.append([f, "", 0])
                break
            scale = 1.0
            for e in child.effects:
                if isinstance(e, otio.schema.LinearTimeWarp):
                    scale *= e.time_scalar
            if skip_speed and scale != 1.0:
                break
            src = child.source_range.start_time.to_seconds() + (t - r.start_time).to_seconds() * scale
            ref = child.media_reference
            name = urllib.parse.unquote(os.path.basename(ref.target_url)) if hasattr(ref, "target_url") else child.name
            out.append([f, name, round(src, 6)])
            break
    return out


def write_others(fixture, out):
    """The fixture as OpenTimelineIO writes each format, and what OpenTimelineIO
    reads back from each file (what the file says plays at sampled frames)."""
    os.makedirs(out, exist_ok=True)
    tl = otio.adapters.read_from_file(fixture)
    fps = float(tl.global_start_time.rate) if tl.global_start_time else 30.0
    written, samples = [], {}
    for name, adapter in [("other.otio", "otio_json"), ("other.xml", "fcp_xml"), ("other.edl", "cmx_3600"), ("other.fcpxml", "fcpx_xml")]:
        t = tl
        if adapter == "cmx_3600":
            t = tl.deepcopy()
            for tr in list(t.tracks):
                if tr.kind != otio.schema.TrackKind.Video:
                    t.tracks.remove(tr)
        path = os.path.join(out, name)
        try:
            otio.adapters.write_to_file(t, path, adapter_name=adapter)
        except Exception as e:  # an OpenTimelineIO writer's own failure, not MetaRush's
            print(f"  OpenTimelineIO's {adapter} writer failed on the fixture ({type(e).__name__}): not read back")
            if os.path.exists(path):
                os.remove(path)
            continue
        try:
            back = otio.adapters.read_from_file(path, adapter_name=adapter, **({"rate": fps} if adapter == "cmx_3600" else {}))
            if isinstance(back, otio.schema.SerializableCollection):
                back = back[0]
            samples[name] = picture_samples(back, fps)
        except Exception as e:
            # OpenTimelineIO can't read its own file: compare with what it
            # meant to write, leaving out speed changes (written approximately).
            print(f"  OpenTimelineIO can't read back its {adapter} file ({type(e).__name__}): compared with the timeline it wrote")
            samples[name] = picture_samples(t, fps, skip_speed=True)
        written.append(name)
    json.dump({"fps": fps, "files": written, "samples": samples}, open(os.path.join(out, "other.expected.json"), "w"))
    print(f"  wrote {', '.join(written)} for MetaRush's readers")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixture", help="folder with fixture.otio, fixture.fcpxml, fixture.xml and fixture.expected.json")
    ap.add_argument("--dtd-dir", required=True)
    ap.add_argument("--other-out", help="write the fixture in every format OpenTimelineIO writes, for MetaRush's readers")
    ap.add_argument("files", nargs="*")
    a = ap.parse_args()
    print(f"OpenTimelineIO {otio.__version__}; adapters: {sorted(x.name for x in otio.plugins.ActiveManifest().adapters)}")
    fcp_dtd, xm_dtd = fetch_dtds(a.dtd_dir)
    todo = []
    if a.fixture:
        for name in sorted(os.listdir(a.fixture)):
            if name.endswith(".expected.json"):
                base = os.path.join(a.fixture, name[: -len(".expected.json")])
                exp = json.load(open(base + ".expected.json"))
                todo += [(base + ext, exp) for ext in (".otio", ".xml", ".fcpxml")]
    todo += [(f, None) for f in a.files]
    for path, exp in todo:
        print(path)
        if path.endswith(".otio"):
            check_otio(path, exp)
        elif path.endswith(".fcpxml"):
            if dtd_valid(path, fcp_dtd):
                print("  FCPXML 1.8 DTD: valid")
            check_fcpxml(path, exp)
        elif path.endswith(".xml"):
            if dtd_valid(path, xm_dtd):
                print("  xmeml v5 DTD: valid")
            check_xmeml(path, exp)
    if a.other_out and a.fixture:
        print("other writers")
        write_others(os.path.join(a.fixture, "fixture.otio"), a.other_out)
    if problems:
        print(f"\n{len(problems)} problem(s) in {checks} checks:")
        for p in problems:
            print(" -", p)
        sys.exit(1)
    print(f"\nall {checks} checks passed")


if __name__ == "__main__":
    main()
