package lj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostURL(t *testing.T) {
	c := NewClient()
	got := c.postURL("news", 166511)
	want := "https://news.livejournal.com/166511.html"
	if got != want {
		t.Errorf("postURL = %q, want %q", got, want)
	}
}

func TestCommentsURL(t *testing.T) {
	c := NewClient()
	got := c.commentsURL("news", 166511, 3)
	want := "https://news.livejournal.com/166511.html?view=flat&page=3&format=light"
	if got != want {
		t.Errorf("commentsURL = %q, want %q", got, want)
	}
}

func TestSetConcurrencyRetunesTransport(t *testing.T) {
	c := NewClient()
	c.SetConcurrency(7)
	if c.HTTPConcurrency != 7 {
		t.Errorf("HTTPConcurrency = %d, want 7", c.HTTPConcurrency)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if tr.MaxConnsPerHost != 7 || tr.MaxIdleConnsPerHost != 7 {
		t.Errorf("pool = %d/%d, want 7/7", tr.MaxConnsPerHost, tr.MaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns < 100 {
		t.Errorf("MaxIdleConns = %d, want >= 100 (warm pool floor)", tr.MaxIdleConns)
	}
	// Above the 100 default the global idle cap must track concurrency.
	c.SetConcurrency(150)
	if tr.MaxIdleConns != 150 {
		t.Errorf("MaxIdleConns = %d, want 150", tr.MaxIdleConns)
	}
	// Sub-1 is floored to 1.
	c.SetConcurrency(0)
	if c.HTTPConcurrency != 1 {
		t.Errorf("HTTPConcurrency = %d, want 1 (floor)", c.HTTPConcurrency)
	}
}

func TestGetStatusErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Get(context.Background(), srv.URL+"/x")
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a *StatusError — callers can't distinguish 404", err)
	}
	if se.Code != 404 {
		t.Errorf("Code = %d, want 404", se.Code)
	}
}

func TestRetryExhaustedWrapsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Get(context.Background(), srv.URL+"/x")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("want wrapped *StatusError{503}, got %v", err)
	}
}

func TestGetRetryOn429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 1 {
			w.Header().Set("Retry-After", "0") // 0 ⇒ ignored, normal backoff
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	resp, err := c.Get(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("want success after a 429 retry: %v", err)
	}
	resp.Body.Close()
	if got := n.Load(); got != 2 {
		t.Errorf("requests = %d, want 2 (429 then 200)", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":     0,
		"5":    5 * time.Second,
		"0":    0,
		"-3":   0,
		"abc":  0,
		"9999": maxRetryAfter, // clamped
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		if r.URL.Path == "/yes" {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)

	ok, err := c.Exists(context.Background(), srv.URL+"/yes")
	if err != nil {
		t.Fatalf("Exists(/yes): %v", err)
	}
	if !ok {
		t.Error("Exists(/yes) = false, want true")
	}

	ok, err = c.Exists(context.Background(), srv.URL+"/no")
	if err != nil {
		t.Fatalf("Exists(/no): %v", err)
	}
	if ok {
		t.Error("Exists(/no) = true, want false")
	}
}

func TestExistsCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Exists(ctx, srv.URL+"/")
	if err == nil {
		t.Error("expected error for canceled context")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "file content here")
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "test.dat")

	// Download new file
	err := c.downloadFile(context.Background(), srv.URL+"/file", dest)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "file content here" {
		t.Errorf("content = %q, want %q", data, "file content here")
	}
}

func TestDownloadFileSkipsExisting(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		fmt.Fprint(w, "new content")
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "existing.dat")

	// Create existing file
	os.WriteFile(dest, []byte("old content"), 0644)

	err := c.downloadFile(context.Background(), srv.URL+"/file", dest)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	// File should NOT be overwritten
	data, _ := os.ReadFile(dest)
	if string(data) != "old content" {
		t.Errorf("file was overwritten: %q", data)
	}

	if requestCount != 0 {
		t.Errorf("made %d requests, expected 0 (skip)", requestCount)
	}
}

// A 0-byte leftover from a crashed run must NOT be treated as 'done' — the
// download has to re-fetch and atomically replace it.
func TestDownloadFileRetriesZeroByteLeftover(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		fmt.Fprint(w, "real content")
	}))
	defer srv.Close()

	c := &Client{
		http:         http.DefaultClient,
		baseURL:      srv.URL,
		retryBackoff: 0,
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "zerobyte.dat")

	// 0-byte file like a crashed/incomplete prior run
	os.WriteFile(dest, []byte{}, 0644)

	if err := c.downloadFile(context.Background(), srv.URL+"/file", dest); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	data, _ := os.ReadFile(dest)
	if string(data) != "real content" {
		t.Errorf("content = %q, want %q", data, "real content")
	}
	if requestCount != 1 {
		t.Errorf("made %d requests, expected 1", requestCount)
	}
}
