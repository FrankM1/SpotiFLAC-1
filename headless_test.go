package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsHeadless(t *testing.T) {
	if !isHeadless([]string{"--headless"}) {
		t.Fatal("--headless should start headless")
	}
	if isHeadless(nil) || isHeadless([]string{"-psn_0_123"}) {
		t.Fatal("no flag should start the normal window")
	}
}

func TestIdleTooLong(t *testing.T) {
	now := time.Now()
	if idleTooLong(now.Add(-time.Hour), now, true, headlessIdleLimit) {
		t.Fatal("a run with a job in progress is never idle")
	}
	if idleTooLong(now.Add(-time.Minute), now, false, headlessIdleLimit) {
		t.Fatal("a minute is not long enough")
	}
	if !idleTooLong(now.Add(-headlessIdleLimit), now, false, headlessIdleLimit) {
		t.Fatal("the limit with nothing to do should quit")
	}
}

func TestDownloadsCountAsActivityButHealthChecksDoNot(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)
	old := time.Now().Add(-time.Hour)
	s.lastActivity = old

	mux.ServeHTTP(httptest.NewRecorder(), newAPIRequest(http.MethodGet, "/health", nil))
	if last, _ := s.activity(); !last.Equal(old) {
		t.Fatal("a health check must not keep a headless run alive")
	}

	body := strings.NewReader(`{"spotifyId":"3n3Ppam7vgaVa1iaRUc9Lp","outputDir":"/tmp/x"}`)
	mux.ServeHTTP(httptest.NewRecorder(), newAPIRequest(http.MethodPost, "/download", body))
	last, busy := s.activity()
	if !last.After(old) || !busy {
		t.Fatalf("a download request is activity and leaves a job queued: last=%v busy=%v", last, busy)
	}
}

func TestFinishedJobsLeaveTheRunIdle(t *testing.T) {
	s := newAPIServer(&App{})
	job := s.enqueue(apiDownloadRequest{SpotifyID: "3n3Ppam7vgaVa1iaRUc9Lp"})
	s.lastActivity = time.Now().Add(-time.Hour)

	s.failJob(job, "not found")
	last, busy := s.activity()
	if busy {
		t.Fatal("a failed job is no longer in progress")
	}
	if time.Since(last) > time.Minute {
		t.Fatal("a job finishing restarts the idle clock")
	}
}

func TestFinishedJobsArePrunedAfterAWhile(t *testing.T) {
	s := newAPIServer(&App{})
	old := s.enqueue(apiDownloadRequest{SpotifyID: "3n3Ppam7vgaVa1iaRUc9Lp"})
	s.failJob(old, "not found")
	s.mu.Lock()
	old.FinishedAt = time.Now().Add(-20 * time.Minute)
	s.mu.Unlock()
	fresh := s.enqueue(apiDownloadRequest{SpotifyID: "4uLU6hMCjMI75M1A2tKUQC"})
	s.failJob(fresh, "not found")
	queued := s.enqueue(apiDownloadRequest{SpotifyID: "7GhIk7Il098yCjg4BQjzvb"})

	if n := s.pruneFinished(10 * time.Minute); n != 1 {
		t.Fatalf("expected the old failed job to go, pruned %d", n)
	}
	s.mu.Lock()
	_, oldThere := s.jobs[old.ID]
	_, freshThere := s.jobs[fresh.ID]
	_, queuedThere := s.jobs[queued.ID]
	s.mu.Unlock()
	if oldThere || !freshThere || !queuedThere {
		t.Fatalf("old=%v fresh=%v queued=%v", oldThere, freshThere, queuedThere)
	}
}
