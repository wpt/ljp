package lj

import (
	"context"
	"errors"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/testuser/2003/10/" {
			fmt.Fprint(w, `<html><body><a href="/100.html">Post</a></body></html>`)
			return
		}
		// Some months return 404 — should be skipped, not fatal
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

func TestFetchFullPostIndexCanceled(t *testing.T) {
	client := newTestClient("http://127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := FetchFullPostIndex(ctx, client, "testuser")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchFullPostIndex error = %v, want context.Canceled", err)
	}
}

func TestFindLastPostID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Index page: ?skip=0 returns three IDs, newest first.
		if r.URL.Query().Get("skip") == "0" {
			fmt.Fprint(w, `<html><body>
				<a href="/3.html">latest</a>
				<a href="/2.html">middle</a>
				<a href="/1.html">oldest</a>
			</body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body></body></html>`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	id, err := FindLastPostID(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FindLastPostID: %v", err)
	}
	if id != 3 {
		t.Errorf("got id %d, want 3 (newest)", id)
	}
}

func TestFindLastPostIDIgnoresStickyPost(t *testing.T) {
	// A pinned/sticky (old) post floats to the top of the index; the newest post
	// is below it. FindLastPostID must return the highest ID, not the topmost.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("skip") == "0" {
			fmt.Fprint(w, `<html><body>
				<a href="/5.html">pinned old</a>
				<a href="/99.html">newest</a>
				<a href="/98.html">older</a>
			</body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body></body></html>`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	id, err := FindLastPostID(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FindLastPostID: %v", err)
	}
	if id != 99 {
		t.Errorf("got id %d, want 99 (highest, ignoring pinned 5)", id)
	}
}

func TestFetchFullPostIndexAllMonthsFail(t *testing.T) {
	// Every month 404s — e.g. a typo'd username or a removed journal. This must
	// surface as an error, not a silent empty success.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	if _, err := FetchFullPostIndex(context.Background(), client, "nope"); err == nil {
		t.Error("expected error when every month fails, got nil")
	}
}

func TestFindLastPostIDNoPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body></body></html>`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	_, err := FindLastPostID(context.Background(), client, "testuser")
	if err == nil {
		t.Error("expected error for empty journal")
	}
}

// TestFetchFullPostIndexParallelDedup: every served month returns the same three
// IDs. With parallel fetches across hundreds of months the seen-map merge must
// still dedupe to exactly those three. Catches races and confirms sort order.
func TestFetchFullPostIndexParallelDedup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>
			<a href="/300.html">x</a>
			<a href="/100.html">x</a>
			<a href="/200.html">x</a>
		</body></html>`)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ids, err := FetchFullPostIndex(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FetchFullPostIndex: %v", err)
	}
	want := []int{100, 200, 300}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], w)
		}
	}
}
