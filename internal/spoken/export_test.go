package spoken

// SameWords is sameWords, for the tests.
var SameWords = sameWords

// Stopped is stopped, for the tests.
var Stopped = stopped

// SetSweepPause runs fn between the sweep's reading and its writing (a race in a test).
func SetSweepPause(fn func()) func() {
	old := sweepPause
	sweepPause = fn
	return func() { sweepPause = old }
}

// VoiceNeeded is voiceNeeded, for the tests.
var VoiceNeeded = voiceNeeded

// WordsWindowOf is wordsWindow, for the tests.
var WordsWindowOf = wordsWindow

// MatchTooLow is matchTooLow, for the tests.
var MatchTooLow = matchTooLow
