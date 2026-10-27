package components

import (
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/tools"
)

// Place is where people add components in the dashboard.
const Place = "/settings/components"

// Missing is the error a feature returns when a component it needs isn't on
// this computer (D326): "Rendering needs the Remotion renderer (about 400
// MB). Add it in Settings → Components.", E_TOOL_MISSING, with the one
// command that adds it in Details and the component's name for the
// dashboard's Add it.
func Missing(name string) *apperr.Error {
	c, ok := Get(name)
	if !ok {
		return apperr.New(apperr.ToolMissing, "Something this needs isn't on this computer.")
	}
	return missingErr(c, c.Needed())
}

func missingErr(c *Component, msg string) *apperr.Error {
	e := apperr.New(apperr.ToolMissing, "%s", msg).WithDetails(map[string]any{"component": c.Name, "place": Place})
	if c.Required {
		return e.WithCommand("metarush setup")
	}
	return e.WithCommand("metarush components add " + c.Name)
}

// ForTool is the component that brings a program (nil when none does).
func ForTool(tool string) *Component {
	for _, c := range registry {
		for _, t := range c.Tools {
			if t == tool {
				return c
			}
		}
	}
	return nil
}

// advise says which component a missing program comes with, in plain words:
// "MetaRush needs FFmpeg to read video and sound files, and it isn't on this
// computer yet (about 45 MB). Add it in Settings → Components."
func advise(tool string) *apperr.Error {
	c := ForTool(tool)
	if c == nil {
		return nil
	}
	what := tools.MissingFor(tool)
	if what == "" {
		return missingErr(c, c.Needed())
	}
	return missingErr(c, "MetaRush needs "+c.Thing+" "+what+", and it isn't on this computer yet ("+c.Size()+"). Add it in Settings → Components.")
}

func init() { tools.Advise = advise }
