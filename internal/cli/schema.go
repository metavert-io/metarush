package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/invopop/jsonschema"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/todos"
)

func schemaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schema", Short: "Show or export the JSON Schemas for edit plans, specs and templates"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List schema documents",
		RunE: func(c *cobra.Command, _ []string) error {
			names := schemaNames()
			return emit(c, map[string]any{"schemas": names}, func() { fmt.Println(strings.Join(names, "\n")) })
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show <name>",
		Short: "Print one schema (e.g. edit-plan, edit-spec, template.lower-third.clean)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name := args[0]
			if !strings.HasSuffix(name, ".schema.json") {
				name += ".schema.json"
			}
			s, ok := editspec.Schemas()[name]
			if !ok {
				return failErr(c, apperr.New(apperr.NotFound, "There's no document called “%s”.", args[0]).
					WithHint("The list shows every one.").WithCommand("metarush schema list"))
			}
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	})
	var dir string
	exp := &cobra.Command{
		Use:   "export",
		Short: "Write every schema to a directory (used to regenerate committed schemas and TypeScript types)",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			all := editspec.Schemas()
			all["ops.schema.json"] = opsSchema()
			all["ability.schema.json"] = abilities.ManifestSchema() // ability.yaml (D230)
			all["api.schema.json"] = apiSchema()
			for name, s := range all {
				b, _ := json.MarshalIndent(s, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
					return err
				}
			}
			// The template catalog shared with the Remotion package (video/ imports it).
			cb, _ := json.MarshalIndent(editspec.Catalog(), "", "  ")
			if err := os.WriteFile(filepath.Join(dir, "templates.json"), append(cb, '\n'), 0o644); err != nil {
				return err
			}
			// The transition catalog (`edit transitions`, the dashboard's transition picker).
			tb, _ := json.MarshalIndent(editspec.TransitionCatalog(), "", "  ")
			if err := os.WriteFile(filepath.Join(dir, "transitions.json"), append(tb, '\n'), 0o644); err != nil {
				return err
			}
			// The caption looks (D245: the renderer, the dashboard and `captions looks`).
			lb, _ := json.MarshalIndent(editspec.CaptionLookFile(), "", "  ")
			if err := os.WriteFile(filepath.Join(dir, "caption-looks.json"), append(lb, '\n'), 0o644); err != nil {
				return err
			}
			return emit(c, map[string]any{"dir": dir, "schemas": schemaNames(), "catalog": "templates.json", "transitions": "transitions.json", "captionLooks": "caption-looks.json"}, func() {
				fmt.Printf("wrote %d schemas, the template catalog, the transition catalog and the caption looks to %s\n", len(all), dir)
			})
		},
	}
	exp.Flags().StringVar(&dir, "dir", "schema", "output directory")
	cmd.AddCommand(exp)
	return cmd
}

// opsSchemaNames gives a distinct schema name to Go types that share a name
// across packages. Definitions are keyed by name, so without an entry the last
// type reflected would silently replace the other in the generated TypeScript.
var opsSchemaNames = map[reflect.Type]string{
	reflect.TypeOf(todos.Counts{}):       "TodoCounts",       // activity.Counts keeps "Counts"
	reflect.TypeOf(providers.StatusIn{}): "ProviderStatusIn", // assistant.StatusIn keeps "StatusIn"
	reflect.TypeOf(cutsheet.Anchor{}):    "WordAnchor",       // editspec.Anchor (narration) keeps "Anchor"
	reflect.TypeOf(sound.ShowOut{}):      "SoundShowOut",     // voiceover.ShowOut keeps "ShowOut"
	reflect.TypeOf(sound.PreviewOut{}):   "SoundPreviewOut",  // voiceover.PreviewOut keeps "PreviewOut"
	// A source's fixes and info card (D151, D152): named for what they are.
	reflect.TypeOf(fixes.View{}):       "FixesView",
	reflect.TypeOf(fixes.Row{}):        "FixRow",
	reflect.TypeOf(fixes.ShowIn{}):     "FixesIn",
	reflect.TypeOf(fixes.SetIn{}):      "FixesSetIn",
	reflect.TypeOf(fixes.LookOut{}):    "FixesLookOut", // generate.LookOut keeps "LookOut"
	reflect.TypeOf(fixes.KindChoice{}): "FixKindChoice",
	reflect.TypeOf(fixes.CopyView{}):   "FixedCopyView",
	reflect.TypeOf(fixes.CutUse{}):     "FixCutUse",
	reflect.TypeOf(fixes.Card{}):       "SourceCard",
	// Estimates of what outside AI services cost (D208): one shape everywhere.
	reflect.TypeOf(costs.Estimate{}): "CostEstimate",
	reflect.TypeOf(costs.Part{}):     "CostPart",
	// AI services (D224–D228): named for what they are.
	reflect.TypeOf(aiwork.MakeIn{}):          "AIMakeIn",
	reflect.TypeOf(aiwork.CopyIn{}):          "AIVoiceCopyIn",
	reflect.TypeOf(aiwork.ConsentView{}):     "AIConsentView",
	reflect.TypeOf(aiwork.WorkOutput{}):      "AIWorkOutput",
	reflect.TypeOf(aiwork.WorkView{}):        "AIWorkView",
	reflect.TypeOf(aiwork.MakeOut{}):         "AIMakeOut",
	reflect.TypeOf(aiwork.ListIn{}):          "AIListIn",
	reflect.TypeOf(aiwork.ListOut{}):         "AIListOut",
	reflect.TypeOf(aiwork.IDIn{}):            "AIWorkIDIn",
	reflect.TypeOf(aiwork.CancelOut{}):       "AICancelOut",
	reflect.TypeOf(aiwork.ModelsIn{}):        "AIModelsIn",
	reflect.TypeOf(aiwork.ModelView{}):       "AIModelView",
	reflect.TypeOf(aiwork.KindView{}):        "AIKindView",
	reflect.TypeOf(aiwork.ProviderView{}):    "AIProviderView",
	reflect.TypeOf(aiwork.ModelsOut{}):       "AIModelsOut",
	reflect.TypeOf(aiwork.VoicesIn{}):        "AIVoicesIn",
	reflect.TypeOf(aiwork.VoiceView{}):       "AIVoiceView",
	reflect.TypeOf(aiwork.VoicesOut{}):       "AIVoicesOut",
	reflect.TypeOf(aiwork.VoiceRemoveIn{}):   "AIVoiceRemoveIn",
	reflect.TypeOf(aiwork.VoiceRemoveOut{}):  "AIVoiceRemoveOut",
	reflect.TypeOf(aiwork.RulesIn{}):         "AIRulesIn",
	reflect.TypeOf(aiwork.RulesOut{}):        "AIRulesOut",
	reflect.TypeOf(aiwork.RulesSetIn{}):      "AIRulesSetIn",
	reflect.TypeOf(aiservices.Alternative{}): "AIAlternative",
	reflect.TypeOf(aiservices.Model{}):       "AIModel",
	reflect.TypeOf(aiservices.Licence{}):     "AILicence",
	reflect.TypeOf(aiservices.Limits{}):      "AIModelLimits",
	reflect.TypeOf(aiservices.PlanTerms{}):   "AIPlanTerms",
	reflect.TypeOf(aiservices.DataTerms{}):   "AIDataTerms",
	reflect.TypeOf(aiservices.KindInfo{}):    "AIKindInfo",
	// Abilities (D230–D235): what a check found and what this computer has.
	reflect.TypeOf(abilities.Problem{}):     "AbilityProblem",
	reflect.TypeOf(abilities.Here{}):        "AbilityHere",
	reflect.TypeOf(abilities.HereProgram{}): "AbilityHereProgram",
	reflect.TypeOf(abilities.HereKey{}):     "AbilityHereKey",
}

// opsSchema describes every operation's input and output (used to generate the
// dashboard's typed API client). It panics when two types would get the same
// definition name (add an opsSchemaNames entry).
func opsSchema() *jsonschema.Schema {
	owner := map[string]reflect.Type{}
	r := &jsonschema.Reflector{AllowAdditionalProperties: false, DoNotReference: false, Namer: func(t reflect.Type) string {
		name := t.Name()
		if n, ok := opsSchemaNames[t]; ok {
			name = n
		}
		if name == "" || t.Kind() != reflect.Struct {
			return name
		}
		if prev, ok := owner[name]; ok && prev != t {
			panic(fmt.Sprintf("ops schema: %s and %s both generate %q; add an opsSchemaNames entry", prev, t, name))
		}
		owner[name] = t
		return name
	}}
	root := &jsonschema.Schema{Version: jsonschema.Version, Title: "OpMap", Type: "object", Definitions: jsonschema.Definitions{}, Properties: jsonschema.NewProperties()}
	ref := func(t reflect.Type) *jsonschema.Schema {
		s := r.ReflectFromType(t)
		for k, v := range s.Definitions {
			root.Definitions[k] = v
		}
		if s.Ref != "" {
			return &jsonschema.Schema{Ref: s.Ref}
		}
		return s
	}
	for _, op := range ops.All() {
		p := &jsonschema.Schema{Type: "object", Properties: jsonschema.NewProperties(), Required: []string{"input", "output"}, AdditionalProperties: jsonschema.FalseSchema, Description: op.Summary}
		p.Properties.Set("input", ref(op.InType))
		p.Properties.Set("output", ref(op.OutType))
		root.Properties.Set(op.Name, p)
		root.Required = append(root.Required, op.Name)
	}
	root.AdditionalProperties = jsonschema.FalseSchema
	return root
}

// apiSchema describes the dashboard's HTTP payloads outside the op registry
// (status, session, uploads, SSE events, peaks, technical evidence, assistant).
func apiSchema() *jsonschema.Schema {
	r := &jsonschema.Reflector{AllowAdditionalProperties: false, DoNotReference: false, Namer: func(t reflect.Type) string {
		if n, ok := api.ContractNames[t]; ok {
			return n
		}
		return t.Name()
	}}
	root := &jsonschema.Schema{Version: jsonschema.Version, Title: "ApiContract", Type: "object", Definitions: jsonschema.Definitions{}, Properties: jsonschema.NewProperties(),
		Description: "HTTP payloads of the local dashboard server outside /api/ops (generated from internal/api ContractTypes)."}
	owner := map[string]reflect.Type{}
	for _, ct := range api.ContractTypes() {
		t := reflect.TypeOf(ct.Value)
		s := r.ReflectFromType(t)
		for k, v := range s.Definitions {
			root.Definitions[k] = v
		}
		if s.Ref != "" {
			name := s.Ref[strings.LastIndex(s.Ref, "/")+1:]
			if prev, ok := owner[name]; ok && prev != t {
				panic(fmt.Sprintf("api contract: %s and %s both generate %q; add a ContractNames entry", prev, t, name))
			}
			owner[name] = t
			root.Properties.Set(ct.Name, &jsonschema.Schema{Ref: s.Ref})
		} else {
			root.Properties.Set(ct.Name, s)
		}
		root.Required = append(root.Required, ct.Name)
	}
	root.AdditionalProperties = jsonschema.FalseSchema
	return root
}

func schemaNames() []string {
	var names []string
	for n := range editspec.Schemas() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
