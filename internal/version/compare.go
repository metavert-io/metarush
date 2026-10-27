package version

import (
	"strconv"
	"strings"
)

// Parsed is a MetaRush version taken apart: 0.2.0, 0.2.0-dev.3, 0.2.0-dev,
// 0.3.0-rc1 (a pre-release) and 0.3.0-rc1-dev.2 (two commits after it).
type Parsed struct {
	Major, Minor, Patch int
	// Pre is a pre-release such as "rc1" ("" for none).
	Pre string
	// Dev: a build after the release, or one from source.
	Dev bool
	// DevCount is how many commits after the release; -1 when not known.
	DevCount int
}

// Release is the release this version is or follows: "0.2.0", "0.3.0-rc1".
func (p Parsed) Release() string {
	s := strconv.Itoa(p.Major) + "." + strconv.Itoa(p.Minor) + "." + strconv.Itoa(p.Patch)
	if p.Pre != "" {
		s += "-" + p.Pre
	}
	return s
}

// Parse reads a version ("v" in front and "+build" metadata are ignored).
func Parse(v string) (Parsed, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, suffix, hasSuffix := strings.Cut(s, "-")
	p := Parsed{DevCount: -1}
	nums := strings.Split(core, ".")
	if len(nums) != 3 {
		return Parsed{}, false
	}
	for i, n := range nums {
		x, err := strconv.Atoi(n)
		if err != nil || x < 0 || n == "" || (len(n) > 1 && n[0] == '0') {
			return Parsed{}, false
		}
		switch i {
		case 0:
			p.Major = x
		case 1:
			p.Minor = x
		default:
			p.Patch = x
		}
	}
	if !hasSuffix {
		return p, true
	}
	if suffix == "" {
		return Parsed{}, false
	}
	dev := ""
	switch {
	case suffix == "dev" || strings.HasPrefix(suffix, "dev."):
		dev = suffix
	default:
		if i := strings.LastIndex(suffix, "-dev"); i >= 0 && (len(suffix) == i+4 || suffix[i+4] == '.') {
			p.Pre, dev = suffix[:i], suffix[i+1:]
		} else {
			p.Pre = suffix
		}
	}
	if p.Pre != "" && !validPre(p.Pre) {
		return Parsed{}, false
	}
	if dev != "" {
		p.Dev = true
		if n, ok := strings.CutPrefix(dev, "dev."); ok {
			x, err := strconv.Atoi(n)
			if err != nil || x < 0 {
				return Parsed{}, false
			}
			p.DevCount = x
		} else if dev != "dev" {
			return Parsed{}, false
		}
	}
	return p, true
}

func validPre(s string) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-') {
				return false
			}
		}
	}
	return true
}

// Compare orders two versions: -1 when a is older than b, 0 when they are the
// same, 1 when a is newer. ok is false when it can't tell: a version that
// doesn't parse, or a build from source (count not known) of the same release
// as the other. A build N commits past a release is newer than the release
// and than one fewer commits past it; releases and pre-releases follow semver
// (0.3.0-rc1 < 0.3.0).
func Compare(a, b string) (int, bool) {
	if strings.TrimPrefix(strings.TrimSpace(a), "v") == strings.TrimPrefix(strings.TrimSpace(b), "v") {
		return 0, a != "" || b != ""
	}
	pa, okA := Parse(a)
	pb, okB := Parse(b)
	if !okA || !okB {
		return 0, false
	}
	for _, c := range []int{cmp(pa.Major, pb.Major), cmp(pa.Minor, pb.Minor), cmp(pa.Patch, pb.Patch), comparePre(pa.Pre, pb.Pre)} {
		if c != 0 {
			return c, true
		}
	}
	if (pa.Dev && pa.DevCount < 0) || (pb.Dev && pb.DevCount < 0) {
		return 0, false
	}
	return cmp(max(pa.DevCount, 0), max(pb.DevCount, 0)), true
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePre orders pre-releases the semver way: none is newer than any;
// dot-separated parts compare numerically when both are numbers, a number is
// older than a word, words compare as text, and more parts are newer.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, errX := strconv.Atoi(as[i])
		y, errY := strconv.Atoi(bs[i])
		switch {
		case errX == nil && errY == nil:
			if c := cmp(x, y); c != 0 {
				return c
			}
		case errX == nil:
			return -1
		case errY == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmp(len(as), len(bs))
}
