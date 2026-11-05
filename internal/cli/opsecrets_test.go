package cli

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretfields"
)

// Every operation's input field whose name often holds a secret without
// saying so (current, new, pin, code…) is declared in internal/secretfields,
// so the activity log hides it, or named in notSecret with why it holds none:
// a new one fails until it is one or the other. Fields whose names say they
// are secret (key, token, password…) are hidden by their names already.
func TestSecretInputFieldsAreDeclared(t *testing.T) {
	suspect := regexp.MustCompile(`^(current|new|old|pass|passcode|pin|otp|phrase|code|ticket|verifier|grant|session|signature|dsn|uri|connection)$`)
	notSecret := map[string]string{
		"ai.rules.set pin":                   "the model each kind of AI work is pinned to",
		"improve.signals entries.error.code": "the error code (E_…) of a log entry being looked at",
	}
	seen := map[string]bool{}
	var walk func(op, path string, ty reflect.Type, depth int)
	walk = func(op, path string, ty reflect.Type, depth int) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct || depth > 6 {
			return
		}
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.Anonymous && name == "" {
				walk(op, path, f.Type, depth)
				continue
			}
			if name == "" || name == "-" {
				continue
			}
			if holdsText(f.Type) && suspect.MatchString(strings.ToLower(name)) && !logbook.SecretName(name) {
				key := op + " " + path + name
				switch {
				case path == "" && secretfields.Of(op)[name]:
				case notSecret[key] != "":
					seen[key] = true
				default:
					t.Errorf("%s: %q may hold a secret: declare it in internal/secretfields, or name it in notSecret with why it holds none", op, path+name)
				}
			}
			ft := f.Type
			for ft.Kind() == reflect.Map {
				ft = ft.Elem()
			}
			walk(op, path+name+".", ft, depth+1)
		}
	}
	for _, o := range ops.All() {
		if o.InType != nil {
			walk(o.Name, "", o.InType, 0)
		}
	}
	for key := range notSecret {
		if !seen[key] {
			t.Errorf("notSecret names %q, which no operation has any more", key)
		}
	}
	// What secretfields declares is an input field of a real operation.
	for op, fields := range secretfields.All() {
		o, ok := ops.Get(op)
		if !ok || o.InType == nil {
			t.Errorf("secretfields declares fields of %q, which isn't an operation", op)
			continue
		}
		ty := o.InType
		for ty.Kind() == reflect.Pointer {
			ty = ty.Elem()
		}
		names := map[string]bool{}
		for i := 0; i < ty.NumField(); i++ {
			names[strings.Split(ty.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for _, f := range fields {
			if !names[f] {
				t.Errorf("secretfields declares %s's %q, which its input doesn't have", op, f)
			}
		}
	}
}

// holdsText says whether a field can carry words: a string, or a list or map
// of them.
func holdsText(ty reflect.Type) bool {
	for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
		ty = ty.Elem()
	}
	return ty.Kind() == reflect.String
}
