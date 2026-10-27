package models

// RenderSource is the stored object a render reads for this asset when the
// version plays the original: the CFR mezzanine for variable-frame-rate video
// once it exists, otherwise the original. Render workers and `cache prefetch`
// both use it, so a prefetched machine has exactly what a render will ask
// for. A version that plays a fixed copy reads RenderSourceFrom instead.
func (a *Asset) RenderSource() (role, key, sha string, size int64) {
	if v, ok := a.OriginalVariant(VariantMezzanine); ok && a.Media.VFR && v.StorageKey != "" {
		return VariantMezzanine, v.StorageKey, v.SHA256, v.Size
	}
	return "original", a.StorageKey, a.SHA256, a.Size
}

// RenderSourceFrom is the stored object a render reads for this asset in a
// version that pins the fixed copy c (D152). Copies of variable-frame-rate
// video are made from the steady copy, so they play as they are.
func (a *Asset) RenderSourceFrom(c *SourceCopy) (role, key, sha string, size int64) {
	if c == nil || c.SHA256 == "" {
		return a.RenderSource()
	}
	if v, ok := c.Variants[VariantMezzanine]; ok && c.Media != nil && c.Media.VFR && v.StorageKey != "" {
		return VariantMezzanine, v.StorageKey, v.SHA256, v.Size
	}
	return "fixed", c.StorageKey, c.SHA256, c.Size
}

// OriginalVariant is a variant (preview copy, thumbnail…) of the original
// file: the asset's own while it plays the original, the ones kept aside
// while a fixed copy's are current (D152).
func (a *Asset) OriginalVariant(kind string) (Variant, bool) {
	if a.Fixes != nil && a.Fixes.Copy != nil {
		v, ok := a.Fixes.OriginalVariants[kind]
		return v, ok
	}
	v, ok := a.Variants[kind]
	return v, ok
}
