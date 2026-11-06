package localdb

import (
	"testing"
	"time"
)

// This computer's own server is waited for 3 s, unless the address names its own waits.
func TestOwnServerWaitsAsTheAddressSays(t *testing.T) {
	for _, c := range []struct {
		uri        string
		find, open time.Duration
	}{
		{"mongodb://127.0.0.1:27150/?directConnection=true", 3 * time.Second, 3 * time.Second},
		{"mongodb://127.0.0.1:27150/?directConnection=true&serverSelectionTimeoutMS=500&connectTimeoutMS=400", 500 * time.Millisecond, 400 * time.Millisecond},
	} {
		o := clientOptions(c.uri)
		if err := o.Validate(); err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if *o.ServerSelectionTimeout != c.find || *o.ConnectTimeout != c.open {
			t.Errorf("%s: waits %v and %v, want %v and %v", c.uri, *o.ServerSelectionTimeout, *o.ConnectTimeout, c.find, c.open)
		}
	}
}
