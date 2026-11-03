package ops

import (
	"strconv"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// RenderShape is the shape a render of a version is drawn in: its version's
// own unless it asked for another preset (D391).
func RenderShape(rd *models.Render, rev *models.Revision) editspec.OutputShape {
	own := rev.Spec.Output.Shape()
	if rd.Preset == "" || rd.Preset == rev.Spec.Output.Preset {
		return own
	}
	if s := (editspec.Output{Preset: rd.Preset}).Shape(); s.Known() {
		return s
	}
	return own
}

// InOwnShape reports a render drawn in its version's own shape: its own
// ratio (a 1080p preset of a 720p version's ratio counts).
func InOwnShape(rd *models.Render, rev *models.Revision) bool {
	return RenderShape(rd, rev).RatioKey() == rev.Spec.Output.Shape().RatioKey()
}

// PickRender is the render of a version to show or send from renders listed
// newest first (D391): the newest in the version's own shape, else the
// newest in another; own says which.
func PickRender(rs []models.Render, rev *models.Revision) (rd *models.Render, own bool) {
	for i := range rs {
		if InOwnShape(&rs[i], rev) {
			return &rs[i], true
		}
	}
	if len(rs) > 0 {
		return &rs[0], false
	}
	return nil, false
}

// OtherShapeWords says that what is shown or sent is a copy in another shape
// than the version's ("" when it is the version's own).
func OtherShapeWords(rd *models.Render, rev *models.Revision, what string) string {
	if rd == nil || InOwnShape(rd, rev) {
		return ""
	}
	own, copy := rev.Spec.Output.Shape(), RenderShape(rd, rev)
	ow, cw := own.Word, copy.Word
	if ow == cw { // the same word (a 4:5 version, a 9:16 copy): the ratios tell them apart
		ow, cw = own.Ratio, copy.Ratio
	}
	return "Version " + strconv.Itoa(rev.Number) + " is " + ow + ", but it has no " + what + " in that shape yet, so its " + cw + " copy is used."
}
