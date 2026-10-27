package localdb

import (
	"strconv"
	"strings"
)

// MongoDB 8 and Linux kernels 6.19 through 7.0.13 (D339). Those kernels
// changed how restartable sequences (rseq) behave, which breaks the memory
// allocator MongoDB 8 is built with: MongoDB 8.0.0–8.0.20 crash within about a
// minute (data may be damaged), and from 8.0.21 MongoDB refuses to start there
// (SERVER-121912, SERVER-125742). Kernels 7.0.14 and later (and 7.1 on)
// restore the old behaviour; MongoDB 8.0.30 starts on them again, and 8.0.35
// also reads the upstream version an Ubuntu kernel is built from, since
// Ubuntu's kernels keep calling themselves 7.0.0 (SERVER-131779). MongoDB 7.0
// runs on every kernel. MetaRush checks the kernel the way the MongoDB it
// starts does, so it can say so plainly before downloading, and before
// starting a server that would only stop.

// Kernel is this computer's Linux kernel as MongoDB reads it. The zero
// Kernel is "not Linux" (or unknown): nothing is refused for it.
type Kernel struct {
	// Release is uname's release ("7.0.0-38-generic", `uname -r`).
	Release string `json:"release"`
	// Version is uname's version ("#38-Ubuntu SMP PREEMPT_DYNAMIC …",
	// `uname -v`): it says whether the kernel is Ubuntu's.
	Version string `json:"version,omitempty"`
	// Signature is the first line of /proc/version_signature on Ubuntu
	// ("Ubuntu 7.0.0-38.38-generic 7.0.14"): the upstream version last.
	Signature string `json:"signature,omitempty"`
}

// ReadKernel reads this computer's kernel (the zero Kernel off Linux). Tests
// in other packages replace it to play a Linux computer.
var ReadKernel = readKernel

// kernelNumbers reads "major.minor.patch" at the start of a release the way
// MongoDB does (strtol: leading spaces skipped, anything after the third
// number ignored, "7.0.0-38-generic" is 7.0.0). ok is false when there aren't
// three numbers: MongoDB then starts anyway.
func kernelNumbers(release string) (v [3]int, ok bool) {
	s := strings.TrimLeft(release, " \t\n\v\f\r")
	for i := 0; i < 3; i++ {
		n := 0
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n == 0 {
			return v, false
		}
		x, err := strconv.Atoi(s[:n])
		if err != nil {
			return v, false
		}
		v[i], s = x, s[n:]
		if i < 2 {
			if !strings.HasPrefix(s, ".") {
				return v, false
			}
			s = s[1:]
		}
	}
	return v, true
}

// inBadRange reports whether a release is from 6.19.0 through 7.0.13.
func inBadRange(release string) bool {
	v, ok := kernelNumbers(release)
	if !ok {
		return false
	}
	return !less(v, [3]int{6, 19, 0}) && !less([3]int{7, 0, 13}, v)
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// upstream is the kernel.org version an Ubuntu kernel says it is built from
// (its version signature's last word), "" when it doesn't say.
func (k Kernel) upstream() string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(k.Signature), "Ubuntu ")
	if !ok {
		return ""
	}
	_, up, ok := strings.Cut(rest, " ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(up)
}

// ubuntuAware reports whether MongoDB version v judges an Ubuntu 7.0 kernel
// by the upstream version it is built from (SERVER-131779: 8.0.35, 8.3.14,
// 9.0.4 and later).
func ubuntuAware(v string) bool {
	switch Series(v) {
	case "8.0":
		return Compare(v, "8.0.35") >= 0
	case "8.1", "8.2":
		return false
	case "8.3":
		return Compare(v, "8.3.14") >= 0
	case "9.0":
		return Compare(v, "9.0.4") >= 0
	}
	return Compare(v, "9.1") >= 0
}

// KernelRefuses reports whether MongoDB version v can't run on kernel k:
// it stops at once (8.0.21 and later) or crashes soon after starting
// (8.0.0–8.0.20). MongoDB before 8.0 runs everywhere.
func KernelRefuses(v string, k Kernel) bool {
	if k.Release == "" || Compare(v, "8.0.0") < 0 {
		return false
	}
	if Series(v) == "8.0" && Compare(v, "8.0.21") >= 0 && Compare(v, "8.0.29") <= 0 {
		// Before 8.0.30 the fixed kernels were refused too.
		n, ok := kernelNumbers(k.Release)
		return ok && !less(n, [3]int{6, 19, 0})
	}
	release := k.Release
	if ubuntuAware(v) && strings.HasPrefix(k.Release, "7.0") && strings.Contains(k.Version, "Ubuntu") {
		if up := k.upstream(); up != "" {
			release = up
		}
	}
	return inBadRange(release)
}

// HasTheFix reports whether the kernel carries the kernel.org fix (7.0.14 or
// later) although its release says otherwise: an Ubuntu kernel that still
// calls itself 7.0.0. A MongoDB that reads Ubuntu's signature (8.0.35 on)
// starts there.
func (k Kernel) HasTheFix() bool {
	n, ok := kernelNumbers(k.upstream())
	return ok && !less(n, [3]int{7, 0, 14})
}

// kernelWhy says why MongoDB version v can't run here and what to do about
// it, in plain words.
func kernelWhy(v string, k Kernel) (why, todo string) {
	why = "MongoDB " + v + " can't start on this computer's Linux kernel (" + k.Release + "): MongoDB 8 stops on kernels 6.19 through 7.0.13 because of a known problem between them."
	if k.HasTheFix() {
		return why, "This kernel already has the fix (it is built from " + k.upstream() + ") but still calls itself " + k.Release +
			", which this MongoDB can't tell; a newer MongoDB that can comes with a MetaRush update."
	}
	return why, "Updating the kernel to 7.0.14 or later fixes it, and so does starting the computer with a kernel older than 6.19."
}

// cantStart is the error of a database (version v) this kernel won't run.
func cantStart(v string, k Kernel) *Error {
	if v == "" {
		v = "8"
	}
	why, todo := kernelWhy(v, k)
	return &Error{Plain: why, Next: todo + " Your projects stay safe in it meanwhile.", Cause: ErrKernel}
}

// upgradeWaits is the error of an upgrade to newer, which this kernel won't
// run yet; the database stays at installed.
func upgradeWaits(installed, newer string, k Kernel) *Error {
	why, todo := kernelWhy(newer, k)
	return &Error{Plain: "the database stays at MongoDB " + installed + " for now. " + why, Next: todo, Cause: ErrKernel}
}

// Words is the whole of it, for a status line.
func (e *Error) Words() string { return strings.TrimSpace(sentenceStart(e.Plain) + " " + e.Next) }

func sentenceStart(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
