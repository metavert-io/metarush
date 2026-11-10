package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// A screen is what a person reads before they type a word to approve, start,
// send or delete something. Every typed decision goes through one (decide, or
// screen.show then screen.decide); a guard test reads the code for others.
//
// What it shows, and the question itself, lose their hidden characters first
// (plainwords.Clean): an AI app, a catalogue, a repository or someone outside
// may have written any of it, and a terminal obeys what it is sent — codes that
// erase a line or write the clipboard, characters that reorder or hide words.
// When it took some out it says so before asking, since what was there is
// itself worth knowing.
type screen struct {
	hidden int // taken out of what it showed (each show says so under it)
	err    error
}

// decide shows what show prints, asks the person to type word and reports
// whether they did. show only prints: it asks nothing, doesn't end the
// process and doesn't look at the terminal (standard output is caught while
// it runs, and shown when it returns).
func decide(show func(), question, word string) bool {
	var s screen
	s.show(show)
	return s.decide(question, word)
}

// show runs f with standard output caught and prints what it printed without
// hidden characters.
func (s *screen) show(f func()) {
	if f == nil || s.err != nil {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		s.err = err
		return
	}
	saved := os.Stdout
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	func() {
		os.Stdout = w
		defer func() {
			os.Stdout = saved
			w.Close()
		}()
		f()
	}()
	b := <-done
	r.Close()
	out, n := plainwords.CleanCount(string(b))
	s.hidden += n
	fmt.Fprint(saved, out)
	if n > 0 {
		// Said right under what lost them, whether or not a question follows.
		fmt.Fprintln(saved, hiddenNote(n, "what's shown above"))
	}
}

// decide asks the person to type word and reports whether they did.
func (s *screen) decide(question, word string) bool {
	if s.err != nil {
		fmt.Println("MetaRush couldn't show what this is about, so nothing is decided.")
		return false
	}
	q, n := plainwords.CleanCount(question)
	if n > 0 {
		fmt.Println(hiddenNote(n, "the question below"))
	}
	return ask(strings.Join(strings.Fields(q), " "), "") == word
}

// yes asks a yes-or-no question about the screen (askYes), saying first when
// hidden characters were taken out of it.
func (s *screen) yes(question string, def bool) bool {
	if s.err != nil {
		fmt.Println("MetaRush couldn't show what this is about, so nothing is decided.")
		return false
	}
	q, n := plainwords.CleanCount(question)
	if n > 0 {
		fmt.Println(hiddenNote(n, "the question below"))
	}
	return askYes(strings.Join(strings.Fields(q), " "), def)
}

// hiddenNote says that hidden characters were taken out of where.
func hiddenNote(n int, where string) string {
	what := "a hidden character"
	if n > 1 {
		what = fmt.Sprintf("%d hidden characters", n)
	}
	return "Note: MetaRush took " + what + " out of " + where + " (codes a terminal obeys, or characters that hide or reorder words). " +
		"Whoever wrote it may want you to see something other than what's there: read it carefully before you decide."
}
