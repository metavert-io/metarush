package cutsheet

import (
	"reflect"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Allowed values come from the edit spec's own jsonschema tags, so the cut
// sheet accepts exactly what the schema accepts and cannot drift from it.

func tagEnum(v any, field string) []string {
	t := reflect.TypeOf(v)
	f, ok := t.FieldByName(field)
	if !ok {
		return nil
	}
	var out []string
	for _, part := range strings.Split(f.Tag.Get("jsonschema"), ",") {
		if e, ok := strings.CutPrefix(part, "enum="); ok {
			out = append(out, e)
		}
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

var (
	enumFPS         = tagEnum(editspec.Output{}, "FPS")
	enumPlatform    = tagEnum(editspec.Output{}, "Platform")
	enumPreset      = tagEnum(editspec.Output{}, "Preset")
	enumStyle       = tagEnum(editspec.Captions{}, "Style")
	enumPosition    = tagEnum(editspec.Captions{}, "Position")
	enumCaptionSize = tagEnum(editspec.Captions{}, "Size")
	enumSource      = tagEnum(editspec.Captions{}, "Source")
	enumFont        = tagEnum(editspec.Brand{}, "Font")
	enumRole        = tagEnum(editspec.AudioTrack{}, "Role")
	enumTemplate    = tagEnum(editspec.Overlay{}, "Template")
	enumTransType   = tagEnum(editspec.Transition{}, "Type")
	enumEvidence    = tagEnum(editspec.Evidence{}, "Kind")
	enumMode        = tagEnum(editspec.Framing{}, "Mode")
)

// jsonNames lists a struct's JSON field names (the {…} block vocabulary).
func jsonNames(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}
