package abilities

import (
	"reflect"

	"github.com/invopop/jsonschema"
)

// ManifestSchema is ability.yaml's JSON Schema, generated from Manifest with
// the manifest's own field names (`metarush schema export` writes it as
// schema/ability.schema.json). The rules a schema can't say (a key's hosts
// among the ability's, a cost's input) are Manifest.Validate's.
func ManifestSchema() *jsonschema.Schema {
	r := &jsonschema.Reflector{FieldNameTag: "yaml", AllowAdditionalProperties: false, DoNotReference: false}
	s := r.ReflectFromType(reflect.TypeOf(Manifest{}))
	s.Title = "ability.yaml"
	s.Description = "An ability's manifest: what it does, what it runs, its inputs and outputs, what it costs, the keys and hosts it uses and what leaves the computer (MetaRush, docs/abilities.md)."
	return s
}
