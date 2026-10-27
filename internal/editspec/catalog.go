package editspec

// TemplateCatalog is schema/templates.json: the single list of vetted templates
// (ID, version, screen region, summary, duration limits) shared by Go and the
// Remotion package. `metarush schema export` writes it from Templates; the video
// package imports it instead of keeping its own copy.
type TemplateCatalog struct {
	GeneratedBy string         `json:"generatedBy"`
	Templates   []TemplateInfo `json:"templates"`
}

// Catalog returns the template catalog.
func Catalog() TemplateCatalog {
	return TemplateCatalog{GeneratedBy: "metarush schema export (internal/editspec Templates); do not edit", Templates: Templates}
}
