package lj

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/time/rate"
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

	c := &Client{
		http:    http.DefaultClient,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

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

	c := &Client{
		http:    http.DefaultClient,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Exists(ctx, srv.URL+"/")
	if err == nil {
		t.Error("expected error for canceled context")
	}
}

func TestDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "file content here")
	}))
	defer srv.Close()

	c := &Client{
		http:         http.DefaultClient,
		limiter:      rate.NewLimiter(rate.Inf, 1),
		baseURL:      srv.URL,
		retryBackoff: 0,
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "test.dat")

	// Download new file
	err := c.DownloadFile(context.Background(), srv.URL+"/file", dest)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
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

	c := &Client{
		http:         http.DefaultClient,
		limiter:      rate.NewLimiter(rate.Inf, 1),
		baseURL:      srv.URL,
		retryBackoff: 0,
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "existing.dat")

	// Create existing file
	os.WriteFile(dest, []byte("old content"), 0644)

	err := c.DownloadFile(context.Background(), srv.URL+"/file", dest)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
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
