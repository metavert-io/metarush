//go:build !darwin && !linux

package transcribe

// processPeakMemory is unknown on other systems (the memory cap then never
// restarts whisper-server; the idle stop still does).
func processPeakMemory(pid int) int64 { return 0 }
