package store

import (
	"context"
	"testing"
)

func TestDropGuard(t *testing.T) {
	d := &DB{Name: "metarush-dev"}
	if err := d.DropTestDatabase(context.Background(), "mongodb://127.0.0.1:27018"); err == nil {
		t.Fatal("must refuse to drop a non-test database")
	}
	d = &DB{Name: TestDBPrefix + "abc"}
	if err := d.DropTestDatabase(context.Background(), "mongodb+srv://u:p@cluster0.example.mongodb.net"); err == nil {
		t.Fatal("must refuse to drop on a remote host")
	}
	if localURI("mongodb://127.0.0.1:27018,evil.example.com:27017") {
		t.Fatal("mixed host list treated as local")
	}
}
