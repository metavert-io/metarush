package evidence

// Parse reads ffmpeg detector output (blackdetect, freezedetect, silencedetect
// and ebur128 summary lines) from stderr into a Report. It lets other packages
// (render QA) run the same detectors with their own thresholds and reuse one
// parser. Open intervals are closed at duration.
func Parse(stderr string, duration float64) *Report {
	r := &Report{Version: Version, Scenes: []Scene{}, Silences: []Span{}, Black: []Span{}, Freezes: []Span{}, Notes: []string{}}
	parseDetectors(stderr, r, duration)
	return r
}
