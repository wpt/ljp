package lj

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchFullPostIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Serve monthly archive pages
		switch path {
		case "/testuser/2003/10/":
			fmt.Fprint(w, `<html><body>
				<a href="/100.html">Post</a>
				<a href="/200.html">Post</a>
			</body></html>`)
		case "/testuser/2003/11/":
			fmt.Fprint(w, `<html><body>
				<a href="/300.html">Post</a>
			</body></html>`)
		case "/testuser/2004/01/":
			fmt.Fprint(w, `<html><body>
				<a href="/400.html">Post</a>
				<a href="/200.html">Post dup</a>
			</body></html>`)
		default:
			// Empty month
			fmt.Fprint(w, `<html><body></body></html>`)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ids, err := FetchFullPostIndex(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FetchFullPostIndex: %v", err)
	}

	// Expect deduplicated, sorted: 100, 200, 300, 400
	if len(ids) != 4 {
		t.Fatalf("index = %v, want 4 elements", ids)
	}
	want := []int{100, 200, 300, 400}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], w)
		}
	}
}

func TestFetchFullPostIndexEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body></body></html>`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ids, err := FetchFullPostIndex(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FetchFullPostIndex: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 ids, got %d", len(ids))
	}
}

func TestFetchFullPostIndexHTTPError(t *testing.T) {
	errorCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/testuser/2003/10/" {
			fmt.Fprint(w, `<html><body><a href="/100.html">Post</a></body></html>`)
			return
		}
		// Some months return 404 — should be skipped, not fatal
		errorCount++
		w.WriteHeader(404)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ids, err := FetchFullPostIndex(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FetchFullPostIndex: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 id, got %d: %v", len(ids), ids)
	}
	if len(ids) > 0 && ids[0] != 100 {
		t.Errorf("ids[0] = %d, want 100", ids[0])
	}
}
