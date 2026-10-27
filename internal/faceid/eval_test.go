//go:build faceeval

package faceid

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Prototype check (go test -tags faceeval): FACE_DIR photos, FACE_HELPER the
// on-screen helper, FACE_LIB onnxruntime, FACE_MODEL SFace; aligned crops go
// to FACE_OUT when set.
func TestEvalPrototype(t *testing.T) {
	dir, helper, lib, model := os.Getenv("FACE_DIR"), os.Getenv("FACE_HELPER"), os.Getenv("FACE_LIB"), os.Getenv("FACE_MODEL")
	if dir == "" {
		t.Skip("set FACE_DIR")
	}
	out, err := exec.Command(helper, "analyze", "--dir", dir, "--landmarks", "--text", "off").Output()
	if err != nil {
		t.Fatal(err)
	}
	e, err := Open(lib, model)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		File  string `json:"file"`
		W, H  int
		Faces []struct {
			Box []float64    `json:"box"`
			LM  [][2]float64 `json:"lm"`
		} `json:"faces"`
	}
	vecs := map[string][]float32{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		best := -1
		for i, f := range r.Faces {
			if len(f.LM) == 5 && (best < 0 || f.Box[2]*f.Box[3] > r.Faces[best].Box[2]*r.Faces[best].Box[3]) {
				best = i
			}
		}
		if best < 0 {
			t.Logf("%s: no face with landmarks", r.File)
			continue
		}
		fh, err := os.Open(filepath.Join(dir, r.File))
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(fh)
		fh.Close()
		if err != nil {
			t.Logf("%s: %v", r.File, err)
			continue
		}
		b := img.Bounds()
		pts, _ := Points(r.Faces[best].LM, b.Dx(), b.Dy())
		v, err := e.Embed(img, pts)
		if err != nil {
			t.Fatal(err)
		}
		vecs[r.File] = v
		if o := os.Getenv("FACE_OUT"); o != "" {
			f, _ := os.Create(filepath.Join(o, strings.TrimSuffix(r.File, ".jpg")+".png"))
			_ = png.Encode(f, AlignedImage(img, pts))
			f.Close()
		}
	}
	names := make([]string, 0, len(vecs))
	for n := range vecs {
		names = append(names, n)
	}
	sort.Strings(names)
	who := func(n string) string { return strings.SplitN(n, "-", 2)[0] }
	var same, diff []float64
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			c := Cosine(vecs[names[i]], vecs[names[j]])
			if who(names[i]) == who(names[j]) {
				same = append(same, c)
			} else {
				diff = append(diff, c)
			}
		}
	}
	sort.Float64s(same)
	sort.Float64s(diff)
	fmt.Printf("faces %d same %d diff %d\n", len(names), len(same), len(diff))
	fmt.Printf("same: min %.3f median %.3f max %.3f\n", same[0], same[len(same)/2], same[len(same)-1])
	fmt.Printf("diff: min %.3f median %.3f max %.3f\n", diff[0], diff[len(diff)/2], diff[len(diff)-1])
}
