package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spotbye/SpotiFLAC/backend"
)

// ytServerLauncher starts the local YouTube relay (cmd/ytserver) when the
// custom Qobuz URL points at a loopback address and nothing answers there.
type ytServerLauncher struct {
	mu  sync.Mutex
	cmd *exec.Cmd
	log *os.File
}

// loopbackRelayAddr returns host:port when rawURL is an http(s) URL on a
// loopback host, or "" otherwise.
func loopbackRelayAddr(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

func relayAnswers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func findYTServerBinary() string {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "ytserver")
		if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
			return sibling
		}
	}
	if path, err := exec.LookPath("ytserver"); err == nil {
		return path
	}
	return ""
}

// relayEnv returns the current environment with Homebrew bin dirs on PATH, so
// a Finder-launched app can still find yt-dlp, ffmpeg and deno.
func relayEnv() []string {
	env := os.Environ()
	extra := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	path := os.Getenv("PATH")
	parts := strings.Split(path, string(os.PathListSeparator))
	for _, dir := range extra {
		found := false
		for _, p := range parts {
			if p == dir {
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, dir)
		}
	}
	newPath := "PATH=" + strings.Join(parts, string(os.PathListSeparator))
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, newPath)
}

// ensure starts the relay for customURL if it is a loopback URL with nothing
// listening. It waits briefly for the relay to come up.
func (l *ytServerLauncher) ensure(customURL string) {
	addr := loopbackRelayAddr(customURL)
	if addr == "" || relayAnswers(addr) {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if relayAnswers(addr) {
		return
	}
	l.stopLocked()

	bin := findYTServerBinary()
	if bin == "" {
		fmt.Printf("[ytserver] binary not found next to the app or on PATH; not starting relay for %s\n", addr)
		return
	}

	cmd := exec.Command(bin, "-addr", addr)
	cmd.Env = relayEnv()
	if dir, err := backend.GetFFmpegDir(); err == nil {
		if f, err := os.OpenFile(filepath.Join(dir, "ytserver.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
			l.log = f
		}
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("[ytserver] failed to start %s: %v\n", bin, err)
		l.closeLog()
		return
	}
	l.cmd = cmd
	go cmd.Wait()
	fmt.Printf("[ytserver] started %s on %s (pid %d)\n", bin, addr, cmd.Process.Pid)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if relayAnswers(addr) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("[ytserver] relay on %s did not answer within 5s\n", addr)
}

func (l *ytServerLauncher) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopLocked()
}

func (l *ytServerLauncher) stopLocked() {
	if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Kill()
	}
	l.cmd = nil
	l.closeLog()
}

func (l *ytServerLauncher) closeLog() {
	if l.log != nil {
		l.log.Close()
		l.log = nil
	}
}

// ensureYTServer starts the relay if the saved custom Qobuz URL needs it.
func (a *App) ensureYTServer() {
	settings, _ := a.LoadSettings()
	a.ytServer.ensure(apiSettingString(settings, "customQobuzApi"))
}
