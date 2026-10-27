// metarush-vision: MetaRush's on-device look at video frames (macOS, Apple Vision).
//
// MetaRush runs it with typed arguments (internal/vision); it never sees model or
// user text. For every JPEG it is given it prints one JSON line on stdout:
// image labels with confidence, animals, people (body rectangles), faces (with
// capture quality and, on request, five landmarks for face recognition, or a
// feature print of the face), on-screen text, and on request where the eye is
// drawn (attention saliency), hands and body poses (their joints), and the
// frame's own feature print. Everything runs on this Mac; nothing is uploaded.
//
// Boxes are [x, y, w, h] and points [x, y], normalized 0–1 with the origin at
// the TOP-LEFT of the picture (Vision's own origin is bottom-left; this helper
// flips it).
//
// Usage:
//   metarush-vision --version
//   metarush-vision analyze (--dir <folder> | <file.jpg>...) [--text accurate|fast|off] [--languages en|auto]
//                           [--landmarks] [--saliency] [--hands] [--pose]
//                           [--face-prints] [--frame-prints] [--labels N] [--min-label 0.0-1.0]
//   metarush-vision mask <picture> <out.png>
//       The picture's subject cut out (its background see-through), as a PNG the
//       size of the picture (macOS 14 or later: Apple Vision's subject lifting).
//       Prints {"found":true,"instances":N}, or {"found":false} when it sees no subject.
//
// Build: swiftc -O -o metarush-vision main.swift   (Xcode Command Line Tools, macOS 12+)

import Foundation
import CoreGraphics
import CoreImage
import CoreVideo
import ImageIO
import Vision

/// The output format. MetaRush refuses a helper whose protocol it does not know.
/// 2: --landmarks, --saliency, --hands, --pose and --languages (protocol 1 output is unchanged).
let protocolVersion = 2

/// How much picture around a face goes into its feature print (1 = the face box).
var faceMargin = 1.35

struct Options {
    var files: [String] = []
    var dir: String?
    var text = "accurate"
    var languages = "en"
    var landmarks = false
    var saliency = false
    var hands = false
    var pose = false
    var facePrints = false
    var framePrints = false
    var maxLabels = 12
    var minLabel: Float = 0.12
}

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(2)
}

func parse(_ argv: [String]) -> Options {
    var o = Options()
    var i = 0
    func next() -> String {
        i += 1
        if i >= argv.count { fail("missing value for \(argv[i - 1])") }
        return argv[i]
    }
    while i < argv.count {
        let a = argv[i]
        switch a {
        case "--dir": o.dir = next()
        case "--text":
            o.text = next()
            if !["accurate", "fast", "off"].contains(o.text) { fail("--text must be accurate, fast or off") }
        case "--languages":
            o.languages = next()
            if !["en", "auto"].contains(o.languages) { fail("--languages must be en or auto") }
        case "--landmarks": o.landmarks = true
        case "--saliency": o.saliency = true
        case "--hands": o.hands = true
        case "--pose": o.pose = true
        case "--face-prints": o.facePrints = true
        case "--face-margin":
            guard let f = Double(next()), f >= 0.5, f <= 3 else { fail("--face-margin must be 0.5–3") }
            faceMargin = f
        case "--frame-prints": o.framePrints = true
        case "--labels":
            guard let n = Int(next()), n >= 0, n <= 100 else { fail("--labels must be 0–100") }
            o.maxLabels = n
        case "--min-label":
            guard let f = Float(next()), f >= 0, f <= 1 else { fail("--min-label must be 0–1") }
            o.minLabel = f
        case "--":
            o.files.append(contentsOf: argv[(i + 1)...])
            i = argv.count
            continue
        default:
            if a.hasPrefix("-") { fail("unknown option \(a)") }
            o.files.append(a)
        }
        i += 1
    }
    return o
}

func r3(_ v: Double) -> Double { (v * 1000).rounded() / 1000 }
func r4(_ v: Double) -> Double { (v * 10000).rounded() / 10000 }

/// A Vision rectangle (origin bottom-left) as [x, y, w, h] from the top-left.
func box(_ r: CGRect) -> [Double] {
    [r3(Double(r.minX)), r3(Double(1 - r.maxY)), r3(Double(r.width)), r3(Double(r.height))]
}

func loadImage(_ path: String) -> CGImage? {
    guard let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil) else { return nil }
    return CGImageSourceCreateImageAtIndex(src, 0, nil)
}

/// The feature print of an image (or of a region of it) as base64 little-endian float32.
func featurePrint(_ img: CGImage, region: CGRect? = nil) -> (String, Int)? {
    var input = img
    if let r = region {
        // Crop in pixel space (origin top-left for CGImage), square-ish with a margin.
        let w = Double(img.width), h = Double(img.height)
        let cx = Double(r.midX) * w, cy = (1 - Double(r.midY)) * h
        let side = max(Double(r.width) * w, Double(r.height) * h) * faceMargin
        let rect = CGRect(x: cx - side / 2, y: cy - side / 2, width: side, height: side).integral
            .intersection(CGRect(x: 0, y: 0, width: w, height: h))
        guard rect.width >= 16, rect.height >= 16, let crop = img.cropping(to: rect) else { return nil }
        input = crop
    }
    let req = VNGenerateImageFeaturePrintRequest()
    let handler = VNImageRequestHandler(cgImage: input, options: [:])
    do { try handler.perform([req]) } catch { return nil }
    guard let obs = req.results?.first as? VNFeaturePrintObservation else { return nil }
    var floats = [Float](repeating: 0, count: obs.elementCount)
    switch obs.elementType {
    case .float:
        obs.data.withUnsafeBytes { raw in
            for k in 0..<obs.elementCount { floats[k] = raw.load(fromByteOffset: k * 4, as: Float.self) }
        }
    case .double:
        obs.data.withUnsafeBytes { raw in
            for k in 0..<obs.elementCount { floats[k] = Float(raw.load(fromByteOffset: k * 8, as: Double.self)) }
        }
    default:
        return nil
    }
    let data = floats.withUnsafeBufferPointer { Data(buffer: $0) }
    return (data.base64EncodedString(), obs.elementCount)
}

/// Points of a landmark region in the picture, from the top-left, 0–1.
func points(_ r: VNFaceLandmarkRegion2D?, _ size: CGSize) -> [CGPoint] {
    guard let r = r, r.pointCount > 0 else { return [] }
    return r.pointsInImage(imageSize: size).map { CGPoint(x: Double($0.x) / Double(size.width), y: 1 - Double($0.y) / Double(size.height)) }
}

func centroid(_ ps: [CGPoint]) -> CGPoint? {
    if ps.isEmpty { return nil }
    var x = 0.0, y = 0.0
    for p in ps {
        x += Double(p.x)
        y += Double(p.y)
    }
    return CGPoint(x: x / Double(ps.count), y: y / Double(ps.count))
}

/// The five points face recognition aligns a face by, in the usual order:
/// the eye on the picture's left, the other eye, the tip of the nose, the
/// mouth corner on the picture's left, the other mouth corner.
func fivePoints(_ lm: VNFaceLandmarks2D, _ size: CGSize) -> [[Double]]? {
    let lp = points(lm.leftPupil, size), rp = points(lm.rightPupil, size)
    guard let e1 = lp.first ?? centroid(points(lm.leftEye, size)),
          let e2 = rp.first ?? centroid(points(lm.rightEye, size)) else { return nil }
    let (left, right) = e1.x <= e2.x ? (e1, e2) : (e2, e1)
    // The nose tip: the end of the nose crest farthest from the eyes, else the nose's middle.
    let mid = CGPoint(x: (left.x + right.x) / 2, y: (left.y + right.y) / 2)
    let crest = points(lm.noseCrest, size)
    var nose = centroid(points(lm.nose, size))
    if let a = crest.first, let b = crest.last {
        let da = hypot(Double(a.x - mid.x), Double(a.y - mid.y)), db = hypot(Double(b.x - mid.x), Double(b.y - mid.y))
        nose = da > db ? a : b
    }
    // Mouth corners: the outer lips' ends along the line between the eyes.
    let lips = points(lm.outerLips, size)
    guard let n = nose, lips.count >= 4 else { return nil }
    let ax = Double(right.x - left.x), ay = Double(right.y - left.y)
    let len = max(1e-6, hypot(ax, ay))
    let ux = ax / len, uy = ay / len
    var lo = lips[0], hi = lips[0]
    var loP = Double.infinity, hiP = -Double.infinity
    for p in lips {
        let proj = Double(p.x) * ux + Double(p.y) * uy
        if proj < loP { loP = proj; lo = p }
        if proj > hiP { hiP = proj; hi = p }
    }
    return [left, right, n, lo, hi].map { [r4(Double($0.x)), r4(Double($0.y))] }
}

/// Where the eye is drawn (attention saliency): the heat map's weighted middle
/// and spread, and the most salient regions.
func attention(_ handler: VNImageRequestHandler) -> [String: Any]? {
    let req = VNGenerateAttentionBasedSaliencyImageRequest()
    do { try handler.perform([req]) } catch { return nil }
    guard let obs = req.results?.first as? VNSaliencyImageObservation else { return nil }
    var out: [String: Any] = [:]
    let pb = obs.pixelBuffer
    CVPixelBufferLockBaseAddress(pb, .readOnly)
    defer { CVPixelBufferUnlockBaseAddress(pb, .readOnly) }
    if CVPixelBufferGetPixelFormatType(pb) == kCVPixelFormatType_OneComponent32Float, let base = CVPixelBufferGetBaseAddress(pb) {
        let w = CVPixelBufferGetWidth(pb), h = CVPixelBufferGetHeight(pb), row = CVPixelBufferGetBytesPerRow(pb)
        var sw = 0.0, sx = 0.0, sy = 0.0, peak = 0.0
        for y in 0..<h {
            let line = base.advanced(by: y * row).assumingMemoryBound(to: Float.self)
            for x in 0..<w {
                let v = Double(max(0, line[x]))
                peak = max(peak, v)
                let wt = v * v // sharpen: the hottest places decide
                sw += wt
                sx += wt * (Double(x) + 0.5) / Double(w)
                sy += wt * (Double(y) + 0.5) / Double(h) // rows run from the top
            }
        }
        if sw > 0 {
            let cx = sx / sw, cy = sy / sw
            var vx = 0.0, vy = 0.0
            for y in 0..<h {
                let line = base.advanced(by: y * row).assumingMemoryBound(to: Float.self)
                for x in 0..<w {
                    let v = Double(max(0, line[x]))
                    let wt = v * v
                    let dx = (Double(x) + 0.5) / Double(w) - cx, dy = (Double(y) + 0.5) / Double(h) - cy
                    vx += wt * dx * dx
                    vy += wt * dy * dy
                }
            }
            out["at"] = [r3(cx), r3(cy)]
            out["spread"] = [r3((vx / sw).squareRoot()), r3((vy / sw).squareRoot())]
            out["peak"] = r3(peak)
        }
    }
    var objs: [[String: Any]] = []
    for o in (obs.salientObjects ?? []).sorted(by: { $0.confidence > $1.confidence }).prefix(3) {
        objs.append(["c": r3(Double(o.confidence)), "box": box(o.boundingBox)])
    }
    out["objects"] = objs
    return out
}

let handJoints: [VNHumanHandPoseObservation.JointName] = [
    .wrist, .thumbCMC, .thumbMP, .thumbIP, .thumbTip, .indexMCP, .indexPIP, .indexDIP, .indexTip,
    .middleMCP, .middlePIP, .middleDIP, .middleTip, .ringMCP, .ringPIP, .ringDIP, .ringTip,
    .littleMCP, .littlePIP, .littleDIP, .littleTip,
]

let bodyJoints: [VNHumanBodyPoseObservation.JointName] = [
    .nose, .leftEye, .rightEye, .leftEar, .rightEar, .leftShoulder, .rightShoulder, .neck,
    .leftElbow, .rightElbow, .leftWrist, .rightWrist, .leftHip, .rightHip, .root,
    .leftKnee, .rightKnee, .leftAnkle, .rightAnkle,
]

/// Joints as [x, y, confidence] in a fixed order (confidence 0 when not found).
func joints<K: Hashable>(_ all: [K: VNRecognizedPoint], _ order: [K]) -> [[Double]] {
    order.map { k in
        guard let p = all[k], p.confidence > 0 else { return [0, 0, 0] }
        return [r3(Double(p.location.x)), r3(Double(1 - p.location.y)), r3(Double(p.confidence))]
    }
}

func analyze(_ path: String, _ o: Options) -> [String: Any] {
    var out: [String: Any] = ["file": (path as NSString).lastPathComponent]
    guard let img = loadImage(path) else {
        out["error"] = "could not read the image"
        return out
    }
    out["w"] = img.width
    out["h"] = img.height
    let size = CGSize(width: img.width, height: img.height)
    let classify = VNClassifyImageRequest()
    let animals = VNRecognizeAnimalsRequest()
    let humans = VNDetectHumanRectanglesRequest()
    humans.upperBodyOnly = false
    let faces = VNDetectFaceRectanglesRequest()
    var reqs: [VNRequest] = [classify, animals, humans, faces]
    var text: VNRecognizeTextRequest?
    if o.text != "off" {
        let t = VNRecognizeTextRequest()
        t.recognitionLevel = o.text == "fast" ? .fast : .accurate
        t.usesLanguageCorrection = true
        t.minimumTextHeight = 0.02
        if o.languages == "auto" {
            if #available(macOS 13.0, *) {
                t.automaticallyDetectsLanguage = true
            } else if let langs = try? t.supportedRecognitionLanguages() {
                t.recognitionLanguages = langs
            }
        }
        reqs.append(t)
        text = t
    }
    var handReq: VNDetectHumanHandPoseRequest?
    if o.hands {
        let h = VNDetectHumanHandPoseRequest()
        h.maximumHandCount = 4
        reqs.append(h)
        handReq = h
    }
    var poseReq: VNDetectHumanBodyPoseRequest?
    if o.pose {
        let p = VNDetectHumanBodyPoseRequest()
        reqs.append(p)
        poseReq = p
    }
    let handler = VNImageRequestHandler(cgImage: img, options: [:])
    do { try handler.perform(reqs) } catch {
        out["error"] = "vision: \(error.localizedDescription)"
        return out
    }
    // Labels: the most confident first, above the floor.
    var labels: [[String: Any]] = []
    for obs in (classify.results ?? []).sorted(by: { $0.confidence > $1.confidence }) {
        if labels.count >= o.maxLabels || obs.confidence < o.minLabel { break }
        // Precise labels: Apple's own precision/recall curve says this label is reliable here.
        let precise = obs.hasMinimumPrecision(0.7, forRecall: 0.2)
        labels.append(["id": obs.identifier, "c": r3(Double(obs.confidence)), "p": precise])
    }
    out["labels"] = labels
    var an: [[String: Any]] = []
    for obs in animals.results ?? [] {
        if let l = obs.labels.first {
            an.append(["id": l.identifier.lowercased(), "c": r3(Double(l.confidence)), "box": box(obs.boundingBox)])
        }
    }
    out["animals"] = an
    var hs: [[String: Any]] = []
    for obs in humans.results ?? [] {
        hs.append(["c": r3(Double(obs.confidence)), "box": box(obs.boundingBox)])
    }
    out["humans"] = hs
    // Faces, then their capture quality (how usable each face is as a picture)
    // and, on request, their landmarks.
    let faceObs = faces.results ?? []
    var quality: [UUID: Float] = [:]
    var marks: [UUID: (pts: [[Double]], c: Double)] = [:]
    if !faceObs.isEmpty {
        let q = VNDetectFaceCaptureQualityRequest()
        q.inputFaceObservations = faceObs
        var more: [VNRequest] = [q]
        var lmReq: VNDetectFaceLandmarksRequest?
        if o.landmarks {
            let l = VNDetectFaceLandmarksRequest()
            l.inputFaceObservations = faceObs
            more.append(l)
            lmReq = l
        }
        if (try? handler.perform(more)) != nil {
            for f in q.results ?? [] {
                if let v = f.faceCaptureQuality { quality[f.uuid] = v }
            }
            for (i, f) in (lmReq?.results ?? []).enumerated() {
                guard let lm = f.landmarks, let five = fivePoints(lm, size) else { continue }
                // Results follow the faces they were asked about.
                let id = faceObs.contains(where: { $0.uuid == f.uuid }) ? f.uuid : (i < faceObs.count ? faceObs[i].uuid : f.uuid)
                marks[id] = (five, r3(Double(lm.confidence)))
            }
        }
    }
    var fs: [[String: Any]] = []
    for f in faceObs {
        var row: [String: Any] = ["c": r3(Double(f.confidence)), "box": box(f.boundingBox)]
        if let q = quality[f.uuid] { row["q"] = r3(Double(q)) }
        if let y = f.yaw { row["yaw"] = r3(y.doubleValue) }
        if let r = f.roll { row["roll"] = r3(r.doubleValue) }
        if let m = marks[f.uuid] {
            row["lm"] = m.pts
            row["lmc"] = m.c
        }
        if o.facePrints, let (p, n) = featurePrint(img, region: f.boundingBox) {
            row["print"] = p
            row["dims"] = n
        }
        fs.append(row)
    }
    out["faces"] = fs
    if let t = text {
        var ts: [[String: Any]] = []
        for obs in t.results ?? [] {
            if let c = obs.topCandidates(1).first {
                let s = c.string.trimmingCharacters(in: .whitespacesAndNewlines)
                if !s.isEmpty { ts.append(["s": s, "c": r3(Double(c.confidence)), "box": box(obs.boundingBox)]) }
            }
        }
        out["text"] = ts
    }
    if o.saliency, let att = attention(handler) {
        out["attention"] = att
    }
    if let h = handReq {
        var rows: [[String: Any]] = []
        for obs in h.results ?? [] {
            guard let all = try? obs.recognizedPoints(.all) else { continue }
            var row: [String: Any] = ["c": r3(Double(obs.confidence)), "j": joints(all, handJoints)]
            if #available(macOS 12.0, *) {
                switch obs.chirality {
                case .left: row["side"] = "left"
                case .right: row["side"] = "right"
                default: break
                }
            }
            rows.append(row)
        }
        out["hands"] = rows
    }
    if let p = poseReq {
        var rows: [[String: Any]] = []
        for obs in p.results ?? [] {
            guard let all = try? obs.recognizedPoints(.all) else { continue }
            rows.append(["c": r3(Double(obs.confidence)), "j": joints(all, bodyJoints)])
        }
        out["bodies"] = rows
    }
    if o.framePrints, let (p, n) = featurePrint(img) {
        out["print"] = p
        out["dims"] = n
    }
    return out
}

/// The subject of a picture cut out (Remove the background, D152): Apple
/// Vision's foreground instance mask, applied to the picture, written as a PNG.
func subjectMask(_ inPath: String, _ outPath: String) -> Never {
    guard #available(macOS 14.0, *) else { fail("finding the subject of a picture needs macOS 14 or later") }
    guard let img = loadImage(inPath) else { fail("cannot read \(inPath)") }
    let req = VNGenerateForegroundInstanceMaskRequest()
    let handler = VNImageRequestHandler(cgImage: img, options: [:])
    do { try handler.perform([req]) } catch { fail("vision: \(error.localizedDescription)") }
    guard let obs = req.results?.first, !obs.allInstances.isEmpty else {
        print("{\"found\":false}")
        exit(0)
    }
    guard let buf = try? obs.generateMaskedImage(ofInstances: obs.allInstances, from: handler, croppedToInstancesExtent: false) else {
        fail("could not cut the subject out")
    }
    let ci = CIImage(cvPixelBuffer: buf)
    guard let cg = CIContext(options: nil).createCGImage(ci, from: ci.extent) else { fail("could not draw the cut-out") }
    let url = URL(fileURLWithPath: outPath)
    guard let dst = CGImageDestinationCreateWithURL(url as CFURL, "public.png" as CFString, 1, nil) else { fail("cannot write \(outPath)") }
    CGImageDestinationAddImage(dst, cg, nil)
    guard CGImageDestinationFinalize(dst) else { fail("cannot write \(outPath)") }
    print("{\"found\":true,\"instances\":\(obs.allInstances.count)}")
    exit(0)
}

let argv = Array(CommandLine.arguments.dropFirst())
if argv.first == "--version" || argv.first == "version" {
    let v = ProcessInfo.processInfo.operatingSystemVersion
    print("metarush-vision \(protocolVersion) macOS \(v.majorVersion).\(v.minorVersion)")
    exit(0)
}
if argv.first == "mask" {
    let rest = Array(argv.dropFirst()).filter { $0 != "--" }
    guard rest.count == 2 else { fail("usage: metarush-vision mask <picture> <out.png>") }
    subjectMask(rest[0], rest[1])
}
guard argv.first == "analyze" else { fail("usage: metarush-vision analyze (--dir <folder> | <file.jpg>...) [options]") }
let o = parse(Array(argv.dropFirst()))
var files = o.files
if let d = o.dir {
    guard let names = try? FileManager.default.contentsOfDirectory(atPath: d) else { fail("cannot read \(d)") }
    files += names.filter { $0.lowercased().hasSuffix(".jpg") || $0.lowercased().hasSuffix(".jpeg") }.sorted().map { (d as NSString).appendingPathComponent($0) }
}
if files.isEmpty { fail("no images given") }
setvbuf(stdout, nil, _IOLBF, 0)
for f in files {
    autoreleasepool {
        let row = analyze(f, o)
        if let data = try? JSONSerialization.data(withJSONObject: row, options: [.sortedKeys]), let s = String(data: data, encoding: .utf8) {
            print(s)
        }
    }
}
