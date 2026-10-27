package interchange

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
)

// OpenTimelineIO JSON, written with the schema versions OpenTimelineIO 0.14
// knows (Clip.1 with one media_reference, Marker.2 …), the most widely read
// set: newer readers upgrade it on load, older ones (editing apps that bundle
// an older OpenTimelineIO) read it as is. This is what the official library
// writes when asked for its 0.14 schemas.

type otioRT struct {
	Schema string  `json:"OTIO_SCHEMA"`
	Rate   float64 `json:"rate"`
	Value  float64 `json:"value"`
}

type otioRange struct {
	Schema   string `json:"OTIO_SCHEMA"`
	Duration otioRT `json:"duration"`
	Start    otioRT `json:"start_time"`
}

func rt(v float64, rate float64) otioRT {
	return otioRT{Schema: "RationalTime.1", Rate: rate, Value: v}
}

func trange(start, dur, rate float64) *otioRange {
	return &otioRange{Schema: "TimeRange.1", Start: rt(start, rate), Duration: rt(dur, rate)}
}

type otioTimeline struct {
	Schema    string         `json:"OTIO_SCHEMA"`
	Metadata  map[string]any `json:"metadata"`
	Name      string         `json:"name"`
	GlobalSTT otioRT         `json:"global_start_time"`
	Tracks    *otioStack     `json:"tracks"`
}

type otioStack struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Metadata    map[string]any `json:"metadata"`
	Name        string         `json:"name"`
	SourceRange *otioRange     `json:"source_range"`
	Effects     []any          `json:"effects"`
	Markers     []otioMarker   `json:"markers"`
	Enabled     bool           `json:"enabled"`
	Children    []any          `json:"children"`
}

type otioTrack struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Metadata    map[string]any `json:"metadata"`
	Name        string         `json:"name"`
	SourceRange *otioRange     `json:"source_range"`
	Effects     []any          `json:"effects"`
	Markers     []otioMarker   `json:"markers"`
	Enabled     bool           `json:"enabled"`
	Children    []any          `json:"children"`
	Kind        string         `json:"kind"`
}

type otioClip struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Metadata    map[string]any `json:"metadata"`
	Name        string         `json:"name"`
	SourceRange *otioRange     `json:"source_range"`
	Effects     []any          `json:"effects"`
	Markers     []otioMarker   `json:"markers"`
	Enabled     bool           `json:"enabled"`
	MediaRef    otioExtRef     `json:"media_reference"`
}

type otioExtRef struct {
	Schema         string         `json:"OTIO_SCHEMA"`
	Metadata       map[string]any `json:"metadata"`
	Name           string         `json:"name"`
	AvailableRange *otioRange     `json:"available_range"`
	ImageBounds    any            `json:"available_image_bounds"`
	TargetURL      string         `json:"target_url"`
}

type otioGap struct {
	Schema      string         `json:"OTIO_SCHEMA"`
	Metadata    map[string]any `json:"metadata"`
	Name        string         `json:"name"`
	SourceRange *otioRange     `json:"source_range"`
	Effects     []any          `json:"effects"`
	Markers     []otioMarker   `json:"markers"`
	Enabled     bool           `json:"enabled"`
}

type otioTransition struct {
	Schema   string         `json:"OTIO_SCHEMA"`
	Metadata map[string]any `json:"metadata"`
	Name     string         `json:"name"`
	In       otioRT         `json:"in_offset"`
	Out      otioRT         `json:"out_offset"`
	Type     string         `json:"transition_type"`
}

type otioMarker struct {
	Schema   string         `json:"OTIO_SCHEMA"`
	Metadata map[string]any `json:"metadata"`
	Name     string         `json:"name"`
	Color    string         `json:"color"`
	Range    *otioRange     `json:"marked_range"`
	Comment  string         `json:"comment"`
}

type otioTimeWarp struct {
	Schema     string         `json:"OTIO_SCHEMA"`
	Metadata   map[string]any `json:"metadata"`
	Name       string         `json:"name"`
	EffectName string         `json:"effect_name"`
	Enabled    bool           `json:"enabled"`
	Scalar     float64        `json:"time_scalar"`
}

// FileURL is a file:// URL for an absolute path (percent-encoded).
func FileURL(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String()
}

// WriteOTIO writes the timeline as OpenTimelineIO JSON. meta is stored under
// "metarush" in the timeline's metadata.
func WriteOTIO(tl *Timeline, meta map[string]any) ([]byte, []string, error) {
	notes := append([]string(nil), tl.Notes...)
	fps := float64(tl.FPS)
	stack := &otioStack{Schema: "Stack.1", Metadata: map[string]any{}, Name: "tracks", Effects: []any{}, Markers: []otioMarker{}, Enabled: true, Children: []any{}}
	for _, m := range tl.Markers {
		stack.Markers = append(stack.Markers, otioMarker{Schema: "Marker.2", Metadata: map[string]any{"metarush": map[string]any{"kind": m.Kind}}, Name: m.Name,
			Color: markerColor(m.Kind), Range: trange(float64(m.Start), float64(m.Duration), fps), Comment: m.Comment})
	}
	if err := otioTracks(tl, stack, &notes); err != nil {
		return nil, nil, err
	}
	md := map[string]any{"metarush": withDefaults(meta, map[string]any{"width": tl.Width, "height": tl.Height, "fps": tl.FPS})}
	doc := otioTimeline{Schema: "Timeline.1", Metadata: md, Name: tl.Name, GlobalSTT: rt(0, fps), Tracks: stack}
	b, err := json.MarshalIndent(doc, "", "    ")
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, otioNotes(tl)...)
	return append(b, '\n'), dedupe(notes), nil
}

func otioTracks(tl *Timeline, stack *otioStack, notes *[]string) error {
	fps := float64(tl.FPS)
	for _, group := range [][]*Track{tl.Video, tl.Audio} {
		for _, t := range group {
			kind := "Video"
			if t.Kind == KindAudio {
				kind = "Audio"
			}
			tr := &otioTrack{Schema: "Track.1", Metadata: map[string]any{"metarush": map[string]any{"role": t.Role}}, Name: t.Name, Effects: []any{},
				Markers: []otioMarker{}, Enabled: true, Children: []any{}, Kind: kind}
			for _, s := range layoutTrack(t, tl.FPS, notes, false) {
				switch s.kind {
				case segGap:
					tr.Children = append(tr.Children, otioGap{Schema: "Gap.1", Metadata: map[string]any{}, SourceRange: trange(0, float64(s.duration), fps),
						Effects: []any{}, Markers: []otioMarker{}, Enabled: true})
				case segTrans:
					tr.Children = append(tr.Children, otioTransition{Schema: "Transition.1", Metadata: map[string]any{"metarush": transMeta(s)},
						Name: transitionName(s.trans, t.Kind), In: rt(float64(s.before), fps), Out: rt(float64(s.after), fps), Type: "SMPTE_Dissolve"})
				case segItem:
					c, err := otioItem(tl, s)
					if err != nil {
						return err
					}
					tr.Children = append(tr.Children, c)
				}
			}
			stack.Children = append(stack.Children, tr)
		}
	}
	return nil
}

func otioItem(tl *Timeline, s segment) (any, error) {
	fps := float64(tl.FPS)
	it := s.item
	md := itemMeta(it, s)
	effects := []any{}
	if it.Speed != 1 {
		effects = append(effects, otioTimeWarp{Schema: "LinearTimeWarp.1", Metadata: map[string]any{}, EffectName: "LinearTimeWarp", Enabled: true, Scalar: it.Speed})
	}
	if it.Nested != nil {
		inner := &otioStack{Schema: "Stack.1", Metadata: map[string]any{"metarush": md}, Name: it.Name, SourceRange: trange(s.source, float64(s.duration), fps),
			Effects: effects, Markers: []otioMarker{}, Enabled: true, Children: []any{}}
		var ignored []string
		if err := otioTracks(it.Nested, inner, &ignored); err != nil {
			return nil, err
		}
		return inner, nil
	}
	m := it.Media
	if m == nil {
		return nil, fmt.Errorf("“%s” has no footage file", it.Name)
	}
	rate, frames := mediaRate(m, tl.FPS)
	ref := otioExtRef{Schema: "ExternalReference.1", Metadata: map[string]any{"metarush": map[string]any{"assetId": m.ID, "sha256": m.SHA256}}, Name: m.Name,
		AvailableRange: trange(0, frames, rate), TargetURL: FileURL(m.Path)}
	return otioClip{Schema: "Clip.1", Metadata: map[string]any{"metarush": md}, Name: it.Name, SourceRange: trange(s.source, float64(s.duration), fps),
		Effects: effects, Markers: []otioMarker{}, Enabled: !it.Muted, MediaRef: ref}, nil
}

// mediaRate is a file's frame rate and length in frames: its own rate for
// pictures, the timeline's for sound-only files.
func mediaRate(m *Media, fps int) (rate, frames float64) {
	rate = float64(fps)
	if m.HasVideo && m.FPS > 0 {
		rate = m.FPS
	}
	return rate, math.Round(m.Duration * rate)
}

func itemMeta(it *Item, s segment) map[string]any {
	md := map[string]any{"clip": it.Link, "role": it.Role}
	if it.Role != RolePicture {
		md["levelDb"] = it.LevelDB
		if len(it.Keys) > 0 {
			keys := make([][2]float64, 0, len(it.Keys))
			shift := s.start - it.Start // keys are relative to MetaRush's window
			for _, k := range it.Keys {
				keys = append(keys, [2]float64{float64(k.Frame - shift), k.DB})
			}
			md["levelKeys"] = keys
		}
	}
	if it.FadeIn > 0 {
		md["fadeInFrames"] = it.FadeIn
	}
	if it.FadeOut > 0 {
		md["fadeOutFrames"] = it.FadeOut
	}
	if f := it.Framing; f != nil && it.Role == RolePicture {
		md["framing"] = map[string]any{"mode": f.Mode, "focusX": f.FocusX, "focusY": f.FocusY, "zoom": f.Zoom}
	}
	return md
}

func transMeta(s segment) map[string]any {
	md := map[string]any{"kind": s.trans}
	if s.from != "" {
		md["transition"] = s.from
	}
	if s.dir != "" {
		md["direction"] = s.dir
	}
	if s.easing != "" {
		md["easing"] = s.easing
	}
	return md
}

// transitionName is what an editing app shows for a transition.
func transitionName(kind, trackKind string) string {
	switch kind {
	case TransDipBlack:
		return "Dip to Black"
	case TransDipWhite:
		return "Dip to White"
	case TransWipe:
		return "Wipe"
	case TransSlide:
		return "Slide"
	case TransPush:
		return "Push"
	case TransZoom:
		return "Zoom"
	case TransWhip:
		return "Whip Pan"
	case TransFadeIn:
		if trackKind == KindAudio {
			return "Fade In"
		}
		return "Fade In from Black"
	case TransFadeOut:
		if trackKind == KindAudio {
			return "Fade Out"
		}
		return "Fade Out to Black"
	case TransAudioCrossfade:
		return "Cross Fade"
	}
	return "Cross Dissolve"
}

func markerColor(kind string) string {
	switch kind {
	case "caption":
		return "BLUE"
	case "graphic":
		return "PURPLE"
	case "narration":
		return "ORANGE"
	}
	return "RED"
}

func otioNotes(tl *Timeline) []string {
	var out []string
	for _, t := range tl.Video {
		for i, c := range t.Cuts {
			if i == 0 || c.Kind == "" || c.Kind == TransDissolve {
				continue
			}
			out = append(out, fmt.Sprintf("the %s at %s is a dissolve of the same length in OpenTimelineIO (it keeps the name)", plainTransition(c.Kind), Clock(t.Items[i].Start, tl.FPS)))
		}
	}
	if hasSound(tl) {
		out = append(out, "sound levels, fades and where the music dips under voices are kept as notes on each clip, because OpenTimelineIO has no volume setting: set the levels in the editing app")
	}
	out = append(out, commonNotes(tl)...)
	return out
}

func hasSound(tl *Timeline) bool {
	for _, t := range tl.Audio {
		if len(t.Items) > 0 {
			return true
		}
	}
	return false
}

// commonNotes are losses every format shares.
func commonNotes(tl *Timeline) []string {
	var out []string
	if reframed(tl) {
		out = append(out, "reframing (the crop and zoom that fit the footage to the frame) is not carried: set position and scale in the editing app")
	}
	speedChanged := false
	for _, t := range tl.Video {
		for _, it := range t.Items {
			if it.Speed != 1 {
				speedChanged = true
			}
		}
	}
	if speedChanged {
		out = append(out, "speed changes are written as a constant speed: check them in the editing app")
	}
	graphics, captions := 0, 0
	for _, m := range tl.Markers {
		switch m.Kind {
		case "graphic":
			graphics++
		case "caption":
			captions++
		}
	}
	switch {
	case graphics == 1:
		out = append(out, "the graphic is a marker with its text: rebuild it with the editing app's titles")
	case graphics > 1:
		out = append(out, fmt.Sprintf("the %d graphics are markers with their text: rebuild them with the editing app's titles", graphics))
	}
	if captions > 0 {
		out = append(out, "captions are markers, and an .srt file you can import as subtitles")
	}
	return out
}

// reframed reports whether any picture is cropped or zoomed to fit the frame.
func reframed(tl *Timeline) bool {
	for _, t := range tl.Video {
		for _, it := range t.Items {
			f, m := it.Framing, it.Media
			if f == nil || m == nil || m.Width == 0 || m.Height == 0 || tl.Width == 0 || tl.Height == 0 {
				continue
			}
			srcAspect := float64(m.Width) / float64(m.Height)
			outAspect := float64(tl.Width) / float64(tl.Height)
			if f.Zoom > 1.001 || (f.Mode != "fit" && math.Abs(srcAspect-outAspect) > 0.01) {
				return true
			}
		}
	}
	return false
}

func plainTransition(kind string) string {
	switch kind {
	case TransDipBlack:
		return "fade through black"
	case TransDipWhite:
		return "fade through white"
	case TransWhip:
		return "whip"
	}
	return kind
}

func withDefaults(m map[string]any, d map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range d {
		out[k] = v
	}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
