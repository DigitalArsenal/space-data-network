package main

import "testing"

func TestIsAdminWebSocketPath(t *testing.T) {
	cases := map[string]bool{
		"/ws":           true,
		"/ws/":          true,
		"/ws/status":    true,
		"/ws/status/":   true,
		"/":             false,
		"/ws/other":     false,
		"/api/v1/id":    false,
		"/ws/statusfoo": false,
	}
	for path, want := range cases {
		if got := isAdminWebSocketPath(path); got != want {
			t.Errorf("isAdminWebSocketPath(%q) = %v, want %v", path, got, want)
		}
	}
}
