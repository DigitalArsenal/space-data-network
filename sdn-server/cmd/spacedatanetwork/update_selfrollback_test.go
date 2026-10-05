package main

import (
	"testing"
	"time"
)

// A nil transport must mean "use the default", not "dereference nil".
//
// http.Client.Transport is an INTERFACE field: assigning a typed nil
// (*http.Transport)(nil) yields a non-nil interface holding a nil pointer, so
// net/http skips its own nil check and panics inside alternateRoundTripper.
// That is exactly how the first live signal-driven self-upgrade died —
// in the helper, before it asked the daemon to stop.
func TestNilLoopbackTransportYieldsAUsableClient(t *testing.T) {
	client := daemonLoopbackClientWith(3*time.Second, nil)
	if client.Transport != nil {
		t.Fatalf("Transport = %#v, want a genuinely absent transport (nil interface), not a typed nil", client.Transport)
	}
	// The panic reproduced through any request; a dial failure is fine, a
	// panic is not.
	_, err := client.Get("http://127.0.0.1:1/")
	if err == nil {
		t.Fatal("expected a dial error against a closed port")
	}
}
