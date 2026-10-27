//go:build darwin

package transcribe

import (
	"encoding/binary"
	"sync"

	"github.com/ebitengine/purego"
)

var (
	procMemOnce   sync.Once
	procPidRusage func(pid int32, flavor int32, buf *byte) int32
)

// rusage_info_v4 (sys/resource.h): 296 bytes; ri_phys_footprint at 72 and
// ri_lifetime_max_phys_footprint at 240 (offsets printed with offsetof
// against the macOS SDK; pinned by TestProcessPeakMemory).
const (
	rusageInfoV4     = 4
	rusageV4Size     = 296
	rusagePhysOffset = 72
	rusagePeakOffset = 240
)

// processPeakMemory is another process's (a child's) peak memory footprint in
// bytes over its lifetime, as Activity Monitor counts memory, GPU buffers
// included; 0 when unknown.
func processPeakMemory(pid int) int64 {
	procMemOnce.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		defer func() { _ = recover() }() // RegisterLibFunc panics on a missing symbol
		purego.RegisterLibFunc(&procPidRusage, h, "proc_pid_rusage")
	})
	if procPidRusage == nil || pid <= 0 {
		return 0
	}
	var buf [rusageV4Size]byte
	if procPidRusage(int32(pid), rusageInfoV4, &buf[0]) != 0 {
		return 0
	}
	peak := binary.LittleEndian.Uint64(buf[rusagePeakOffset:])
	if peak == 0 {
		peak = binary.LittleEndian.Uint64(buf[rusagePhysOffset:])
	}
	return int64(peak)
}
