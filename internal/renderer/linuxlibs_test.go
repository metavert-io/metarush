package renderer

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeELF writes a small 64-bit ELF file whose dynamic section needs libs:
// all debug/elf reads of a program (its DT_NEEDED entries), made here so the
// test runs on any computer.
func writeELF(t *testing.T, path string, libs ...string) {
	t.Helper()
	dynstr := []byte{0}
	var offs []uint64
	for _, l := range libs {
		offs = append(offs, uint64(len(dynstr)))
		dynstr = append(append(dynstr, l...), 0)
	}
	var dynamic bytes.Buffer
	for _, o := range offs {
		_ = binary.Write(&dynamic, binary.LittleEndian, [2]uint64{1, o}) // DT_NEEDED
	}
	_ = binary.Write(&dynamic, binary.LittleEndian, [2]uint64{0, 0}) // DT_NULL
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	const ehsize, shentsize = 64, 64
	dynstrOff := uint64(ehsize)
	dynOff := dynstrOff + uint64(len(dynstr))
	shstrOff := dynOff + uint64(dynamic.Len())
	shOff := shstrOff + uint64(len(shstr))
	var b bytes.Buffer
	b.Write([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(3))  // ET_DYN
	w(uint16(62)) // x86-64
	w(uint32(1))
	w(uint64(0)) // entry
	w(uint64(0)) // no program headers
	w(shOff)
	w(uint32(0))
	w(uint16(ehsize))
	w(uint16(56))
	w(uint16(0))
	w(uint16(shentsize))
	w(uint16(4)) // sections
	w(uint16(3)) // .shstrtab
	b.Write(dynstr)
	b.Write(dynamic.Bytes())
	b.Write(shstr)
	sh := func(name, typ uint32, off, size uint64, link uint32, entsize uint64) {
		w(name)
		w(typ)
		w(uint64(0))
		w(uint64(0))
		w(off)
		w(size)
		w(link)
		w(uint32(0))
		w(uint64(1))
		w(entsize)
	}
	sh(0, 0, 0, 0, 0, 0)
	sh(1, 3, dynstrOff, uint64(len(dynstr)), 0, 0) // .dynstr
	sh(9, 6, dynOff, uint64(dynamic.Len()), 1, 16) // .dynamic
	sh(18, 3, shstrOff, uint64(len(shstr)), 0, 0)  // .shstrtab
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The renderer's browser links against the system's own libraries; the ones
// this computer lacks are named, with the command that adds them for its
// release (D522: a fresh Ubuntu in WSL lacks most of them).
func TestBrowserLibraries(t *testing.T) {
	root := t.TempDir()
	old := LibraryRoot
	LibraryRoot = root
	t.Cleanup(func() { LibraryRoot = old })
	video := filepath.Join(root, "renderer", "video")
	if got := BrowserLibraries(video); got.Checked {
		t.Fatal("no browser downloaded yet: nothing to check")
	}
	bin := filepath.Join(video, "node_modules", ".remotion", "chrome-headless-shell", "linux64", "chrome-headless-shell-linux64", "chrome-headless-shell")
	writeELF(t, bin, "libnss3.so", "libasound.so.2", "libEGL.so", "libc.so.6")
	// Chrome's own folder carries some ($ORIGIN); the system has libc only.
	if err := os.WriteFile(filepath.Join(filepath.Dir(bin), "libEGL.so"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"lib/x86_64-linux-gnu", "lib/aarch64-linux-gnu", "etc/ld.so.conf.d", "opt/extra"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"lib/x86_64-linux-gnu/libc.so.6", "lib/aarch64-linux-gnu/libc.so.6"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := BrowserLibraries(video)
	if !got.Checked || strings.Join(got.Missing, ",") != "libasound.so.2,libnss3.so" || got.OK() {
		t.Fatalf("missing %+v", got)
	}
	if !strings.HasPrefix(got.Command, "sudo apt install -y ") || !strings.Contains(got.Command, " libasound2t64 ") || !strings.Contains(got.Command, " libnss3 ") {
		t.Fatalf("command %q", got.Command)
	}
	// Added since (in a folder ld.so.conf.d names): found, and the answer is fresh.
	if err := os.WriteFile(filepath.Join(root, "etc/ld.so.conf.d/extra.conf"), []byte("# extra\n/opt/extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"opt/extra/libnss3.so", "opt/extra/libasound.so.2"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := BrowserLibraries(video); !got.Checked || !got.OK() {
		t.Fatalf("after adding them: %+v", got)
	}
}

func TestLibraryCommand(t *testing.T) {
	for osr, want := range map[string]string{
		"ID=ubuntu\nVERSION_ID=\"22.04\"\n":         " libasound2 ",
		"ID=ubuntu\nVERSION_ID=\"26.04\"\n":         " libasound2t64 ",
		"ID=debian\nVERSION_ID=\"12\"\n":            " libcups2 ",
		"ID=debian\nVERSION_ID=\"13\"\n":            " libcups2t64 ",
		"ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n": "sudo apt install -y libnss3",
		"ID=fedora\nVERSION_ID=43\n":                "Install your system's packages that provide: libnss3.so",
	} {
		if got := libraryCommand(osr, []string{"libnss3.so"}); !strings.Contains(got+" ", want) {
			t.Errorf("%q: %q", osr, got)
		}
	}
}
