package models

import (
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/oldnames"
)

// Fields D210 renamed (internal/oldnames): a document written before it, or by
// an older build since, still has the old name. These decoders read it before
// the new one — only older builds write it, so a document that has both got it
// last — and every write uses the new name only.

// readPlain decodes doc into v the default way (v's type has none of these
// methods); a null, which arrives as no bytes, is the zero value, as the
// default decoder makes it.
func readPlain[T any](doc []byte, v *T) error {
	if len(doc) == 0 {
		var zero T
		*v = zero
		return nil
	}
	return bson.Unmarshal(doc, v)
}

// oldField is a document's field under its old name.
func oldField(doc []byte, name string) (bson.RawValue, bool) {
	v, err := bson.Raw(doc).LookupErr(name)
	return v, err == nil
}

// oldDocument reads a document under its old name into v (the zero value
// when the old name holds anything else, such as null).
func oldDocument[T any](v bson.RawValue, into *T) error {
	var zero T
	*into = zero
	if v.Type != bson.TypeEmbeddedDocument {
		return nil
	}
	return v.Unmarshal(into)
}

// UnmarshalBSON reads a source's fixes under either name of their fingerprint.
func (f *SourceFixes) UnmarshalBSON(doc []byte) error {
	type plain SourceFixes
	if err := readPlain(doc, (*plain)(f)); err != nil {
		return err
	}
	if v, ok := oldField(doc, oldnames.Fingerprint); ok {
		f.Fingerprint, _ = v.StringValueOK()
	}
	return nil
}

// UnmarshalBSON reads the fixed copy a source uses under either name of its
// fingerprint.
func (r *FixedCopyRef) UnmarshalBSON(doc []byte) error {
	type plain FixedCopyRef
	if err := readPlain(doc, (*plain)(r)); err != nil {
		return err
	}
	if v, ok := oldField(doc, oldnames.Fingerprint); ok {
		r.Fingerprint, _ = v.StringValueOK()
	}
	return nil
}

// UnmarshalBSON reads a fixed copy under either name of its fingerprint and
// its fix steps.
func (c *SourceCopy) UnmarshalBSON(doc []byte) error {
	type plain SourceCopy
	if err := readPlain(doc, (*plain)(c)); err != nil {
		return err
	}
	if v, ok := oldField(doc, oldnames.Fingerprint); ok {
		c.Fingerprint, _ = v.StringValueOK()
	}
	if v, ok := oldField(doc, oldnames.FixSteps); ok {
		return oldDocument(v, &c.FixSteps)
	}
	return nil
}

// UnmarshalBSON reads a resource version's source under either name of how a
// generated one was made.
func (s *ResourceSource) UnmarshalBSON(doc []byte) error {
	type plain ResourceSource
	if err := readPlain(doc, (*plain)(s)); err != nil {
		return err
	}
	if v, ok := oldField(doc, oldnames.MadeWith); ok {
		s.MadeWith = nil
		if v.Type == bson.TypeEmbeddedDocument {
			s.MadeWith = &ResourceMadeWith{}
			return oldDocument(v, s.MadeWith)
		}
	}
	return nil
}

// UnmarshalBSON reads a Remotion refresh under either name of its steps.
func (r *RemotionRefresh) UnmarshalBSON(doc []byte) error {
	type plain RemotionRefresh
	if err := readPlain(doc, (*plain)(r)); err != nil {
		return err
	}
	if v, ok := oldField(doc, oldnames.RefreshSteps); ok {
		return oldDocument(v, &r.RefreshSteps)
	}
	return nil
}
