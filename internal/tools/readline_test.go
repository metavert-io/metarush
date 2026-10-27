package tools

import (
	"bufio"
	"strings"
	"testing"
)

// One overlong line must be skipped without stalling the rest of the stream.
func TestReadLineSkipsOverlongLines(t *testing.T) {
	in := "first\n" + strings.Repeat("x", 300) + "\n\nlast-without-newline"
	br := bufio.NewReaderSize(strings.NewReader(in), 16)
	var got []string
	for {
		line, err := readLine(br, 100)
		if line != nil {
			got = append(got, string(line))
		}
		if err != nil {
			break
		}
	}
	want := []string{"first", "", "last-without-newline"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}
