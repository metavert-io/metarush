package cli

import "testing"

// Two Go types with the same name in different packages (activity.Counts and
// todos.Counts) must not collapse into one TypeScript type.
func TestOpsSchemaDefinitionNamesAreUnique(t *testing.T) {
	s := opsSchema() // panics when two types would share a definition name
	for name, field := range map[string]string{"Counts": "uploads", "TodoCounts": "byKind"} {
		d := s.Definitions[name]
		if d == nil {
			t.Fatalf("definition %q missing", name)
		}
		if _, ok := d.Properties.Get(field); !ok {
			t.Fatalf("definition %q has no %q: the wrong type got the name", name, field)
		}
	}
}
