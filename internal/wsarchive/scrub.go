package wsarchive

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"

	"github.com/metavert-io/metarush/internal/logbook"
)

// Scrubber takes keys, passwords and other credentials out of every text in
// a document before it goes into an archive (D330): the activity log's own
// redactor, with the exact secrets this computer knows (the database's
// password, the storage's keys, provider keys) and the credential shapes it
// recognizes anywhere (passwords in addresses, private keys, cloud and API
// keys, bearer tokens, signed addresses). MetaRush keeps no credential in the
// database, so this catches what people pasted into words of their own (a
// comment, a message to Claude).
type Scrubber struct {
	red *logbook.Redactor
}

// NewScrubber builds a scrubber that also hides the given exact values.
func NewScrubber(red *logbook.Redactor, exact ...string) *Scrubber {
	if red == nil {
		red = logbook.NewRedactor()
		red.FromEnvironment()
	}
	red.Add(exact...)
	return &Scrubber{red: red}
}

// Scrubbed is one place a document's text was changed.
type Scrubbed struct {
	ID    string `json:"id"`
	Field string `json:"field"`
}

// Doc returns the document with every text scrubbed and the fields it
// changed (nil when nothing was). Every other value is copied byte for byte,
// so a document without credentials comes out exactly as it went in.
func (s *Scrubber) Doc(raw bson.Raw) (bson.Raw, []string, error) {
	if s == nil {
		return raw, nil, nil
	}
	var changed []string
	out, did, err := s.rewrite(bsoncore.Document(raw), "", &changed)
	if err != nil || !did {
		return raw, nil, err
	}
	return bson.Raw(out), changed, nil
}

// rewrite copies a document, scrubbing strings; it reports whether anything changed.
func (s *Scrubber) rewrite(doc bsoncore.Document, prefix string, changed *[]string) ([]byte, bool, error) {
	elems, err := doc.Elements()
	if err != nil {
		return nil, false, err
	}
	// Built as it goes; when nothing changed the caller keeps the original bytes.
	idx, dst := bsoncore.AppendDocumentStart(nil)
	did := false
	for _, e := range elems {
		key := e.Key()
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		v := e.Value()
		switch v.Type {
		case bsoncore.TypeString:
			str, _ := v.StringValueOK()
			if clean := s.red.String(str); clean != str {
				dst = bsoncore.AppendStringElement(dst, key, clean)
				*changed = append(*changed, path)
				did = true
				continue
			}
		case bsoncore.TypeEmbeddedDocument, bsoncore.TypeArray:
			var inner bsoncore.Document
			if v.Type == bsoncore.TypeArray {
				a, _ := v.ArrayOK()
				inner = bsoncore.Document(a)
			} else {
				inner, _ = v.DocumentOK()
			}
			sub, subChanged, err := s.rewrite(inner, path, changed)
			if err != nil {
				return nil, false, err
			}
			if subChanged {
				dst = bsoncore.AppendHeader(dst, v.Type, key)
				dst = append(dst, sub...)
				did = true
				continue
			}
		}
		dst = bsoncore.AppendValueElement(dst, key, v)
	}
	dst, err = bsoncore.AppendDocumentEnd(dst, idx)
	if err != nil {
		return nil, false, err
	}
	return dst, did, nil
}

// idString is a document's _id in words (for the manifest's list of scrubbed places).
func idString(raw bson.Raw) string {
	v, err := raw.LookupErr("_id")
	if err != nil {
		return ""
	}
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	if o, ok := v.ObjectIDOK(); ok {
		return o.Hex()
	}
	if i, ok := v.Int32OK(); ok {
		return fmt.Sprint(i)
	}
	if i, ok := v.Int64OK(); ok {
		return fmt.Sprint(i)
	}
	return v.String()
}
