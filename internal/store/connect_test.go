package store

import (
	"context"
	"testing"
	"time"
)

// An address that names its own waits is taken at its word; one that doesn't gets MetaRush's 8 s.
func TestAnAddressSaysHowLongToWait(t *testing.T) {
	ms := time.Millisecond
	for _, c := range []struct {
		uri        string
		find, open time.Duration
	}{
		{"mongodb://127.0.0.1:27018/?replicaSet=rs0&directConnection=true", DefaultWait, DefaultWait},
		{"mongodb://127.0.0.1:27018/?serverSelectionTimeoutMS=300&connectTimeoutMS=200", 300 * ms, 200 * ms},
		{"mongodb://127.0.0.1:27018/?serverSelectionTimeoutMS=30000", 30 * time.Second, DefaultWait},
		{"mongodb://127.0.0.1:27018/?connectTimeoutMS=1500", DefaultWait, 1500 * ms},
		// Zero would mean "don't wait at all" to the driver: MetaRush waits as usual.
		{"mongodb://127.0.0.1:27018/?serverSelectionTimeoutMS=0&connectTimeoutMS=0", DefaultWait, DefaultWait},
		// MetaRush names itself whatever the address says.
		{"mongodb://127.0.0.1:27018/?appName=Cluster0", DefaultWait, DefaultWait},
	} {
		o := ClientOptions(c.uri)
		if err := o.Validate(); err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if *o.ServerSelectionTimeout != c.find || *o.ConnectTimeout != c.open {
			t.Errorf("%s: waits %v to find the server and %v to connect, want %v and %v", c.uri, *o.ServerSelectionTimeout, *o.ConnectTimeout, c.find, c.open)
		}
		if o.AppName == nil || *o.AppName != "metarush" {
			t.Errorf("%s: names itself %v", c.uri, o.AppName)
		}
	}
}

// A server that isn't there is given up on as soon as the address says, not after MetaRush's own 8 s.
func TestAnUnreachableServerIsGivenUpOnAsTheAddressSays(t *testing.T) {
	start := time.Now()
	db, err := Connect(context.Background(), "mongodb://127.0.0.1:1/?directConnection=true&serverSelectionTimeoutMS=300&connectTimeoutMS=300", "metarush-test-unreachable")
	if err == nil {
		_ = db.Close(context.Background())
		t.Fatal("connected to a server that isn't there")
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("gave up after %v; the address said 300 ms", took)
	}
}
