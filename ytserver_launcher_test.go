package main

import (
	"strings"
	"testing"
)

func TestLoopbackRelayAddr(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8790":     "127.0.0.1:8790",
		"http://localhost:8790/":    "localhost:8790",
		"https://127.0.0.1":         "127.0.0.1:443",
		"http://127.0.0.1":          "127.0.0.1:80",
		"https://qobuz.example.com": "",
		"http://192.168.1.5:8790":   "",
		"ftp://127.0.0.1:8790":      "",
		"":                          "",
	}
	for in, want := range cases {
		if got := loopbackRelayAddr(in); got != want {
			t.Errorf("loopbackRelayAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRelayEnvAddsHomebrewPaths(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	var path string
	count := 0
	for _, kv := range relayEnv() {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one PATH entry, got %d", count)
	}
	for _, dir := range []string{"/usr/bin", "/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(path, dir) {
			t.Errorf("PATH %q missing %s", path, dir)
		}
	}
}

func TestEnsureSkipsNonLoopback(t *testing.T) {
	var l ytServerLauncher
	l.ensure("https://qobuz.example.com")
	if l.cmd != nil {
		t.Fatal("expected no relay process for a remote URL")
	}
}
