package editspec

import (
	"math"
	"strconv"
	"strings"
)

// Choices MetaRush makes for new things from a shape (stage 2 of the shape
// work, D385–D393). Like the shape rule itself (D376), they choose and show:
// none of them is read when a saved version is drawn, so its plan stays what
// it was (D383, TestPlansAtEveryShapeStayTheSame).

// Phone platforms: their margins (platformSafe) are for a tall picture with an
// app's buttons over it.
var phonePlatforms = map[string]bool{"shorts": true, "tiktok": true, "reels": true}

// PlatformFor is the platform a video of a shape goes to, given the one it
// had (D387): a phone platform (shorts, tiktok, reels) only for vertical —
// YouTube's vertical videos are Shorts, and Shorts made wide go to YouTube —
// and otherwise what it was ("" stays "": generic).
func PlatformFor(s Shape, was string) string {
	switch {
	case s == ShapeVertical && was == "youtube":
		return "shorts"
	case s == ShapeLandscape && was == "shorts":
		return "youtube"
	case s != ShapeVertical && phonePlatforms[was]:
		return "generic"
	}
	return was
}

// PlatformShape is the shape a platform is made for ("" for generic or none):
// what a brief's platforms say about a project's shape (D385).
func PlatformShape(platform string) Shape {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "youtube":
		return ShapeLandscape
	case "shorts", "tiktok", "reels":
		return ShapeVertical
	}
	return ""
}

// ParseRatio reads an aspect ratio like "16:9" (zeros when it isn't one).
func ParseRatio(r string) (w, h int) {
	a, b, ok := strings.Cut(strings.TrimSpace(r), ":")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, 0
	}
	return x, y
}

// RatioFor is the aspect ratio a service offers ("16:9", "9:16", "1:1"…)
// closest to a w×h frame (D385): one of the frame's own shape first, then
// the nearest by how much would be cropped (equal steps either way count
// alike); a tie goes to fallback (the service's own default), else the first
// offered. "" when nothing is offered or the frame has no size.
func RatioFor(w, h int, offered []string, fallback string) string {
	want := ShapeOf(w, h)
	if want == "" {
		return ""
	}
	r := math.Log(float64(w) / float64(h))
	best, bestSame, bestD := "", false, math.Inf(1)
	better := func(o string, same bool, d float64) bool {
		switch {
		case best == "":
			return true
		case same != bestSame:
			return same
		case math.Abs(d-bestD) > 1e-9:
			return d < bestD
		}
		return o == fallback // as near either way: the service's own default
	}
	for _, o := range offered {
		x, y := ParseRatio(o)
		if x == 0 {
			continue
		}
		same := ShapeOf(x, y) == want
		d := math.Abs(math.Log(float64(x)/float64(y)) - r)
		if better(o, same, d) {
			best, bestSame, bestD = o, same, d
		}
	}
	return best
}

// CutSizeFor is the size a new cut of a w×h frame renders at (D389): the
// preset of its ratio when it has one (16:9, 9:16, 1:1: 1080p, as always),
// else its own ratio with the short side 1080 (4:5 → 1080×1350), both sides
// even and at most 4096. exact is false when the ratio couldn't be kept in
// whole even numbers (2048×858 → 2578×1080).
func CutSizeFor(w, h int) (out Output, exact bool) {
	if w <= 0 || h <= 0 {
		return Output{}, false
	}
	key := ShapeOfSize(w, h).RatioKey()
	for _, p := range []string{PresetLandscape, PresetVertical, PresetSquare} {
		if (Output{Preset: p}).Shape().RatioKey() == key {
			return Output{Preset: p}, true
		}
	}
	scale := 1080 / float64(min(w, h))
	if long := float64(max(w, h)) * scale; long > 4096 {
		scale = 4096 / float64(max(w, h))
	}
	even := func(v float64) int { return max(64, int(math.Round(v/2))*2) }
	cw, ch := even(float64(w)*scale), even(float64(h)*scale)
	return Output{Width: cw, Height: ch}, ShapeOfSize(cw, ch).RatioKey() == key
}
