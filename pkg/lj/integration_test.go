package lj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Shared helpers (testPostHTML, testCommentsHTML, newTestClient, queryPage)
// live in testhelpers_test.go.

func TestParsePostFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("format") == "light" {
			page := 1
			if p := query.Get("page"); p != "" {
				if n, err := strconv.Atoi(p); err == nil {
					page = n
				}
			}
			fmt.Fprint(w, testCommentsHTML(page, 2))
			return
		}
		fmt.Fprint(w, testPostHTML)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ctx := context.Background()
	post, err := ParsePost(ctx, client, "testuser", 12345)
	if err != nil {
		t.Fatalf("ParsePost: %v", err)
	}
	if post.Title != "Test Post Title" {
		t.Errorf("title = %q", post.Title)
	}
	if post.Date != "January 15 2020, 10:30" {
		t.Errorf("date = %q", post.Date)
	}
	if !strings.Contains(post.Body, "Hello world") {
		t.Errorf("body = %q, want to contain 'Hello world'", post.Body)
	}
	if len(post.Tags) != 2 {
		t.Errorf("tags = %v", post.Tags)
	}
	if post.OG == nil || post.OG.Title != "Test Post" {
		t.Errorf("og = %v", post.OG)
	}
	if post.ID != 12345 {
		t.Errorf("id = %d", post.ID)
	}
}

func TestParseCommentsFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := queryPage(r)
		fmt.Fprint(w, testCommentsHTML(page, 2))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	ctx := context.Background()
	tree, err := ParseComments(ctx, client, "testuser", 12345)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}

	// Should have 2 root comments: "first comment" (with 1 child) and "page two comment"
	if len(tree) != 2 {
		t.Fatalf("roots = %d, want 2", len(tree))
	}
	if tree[0].Body != "first comment" {
		t.Errorf("root[0] body = %q", tree[0].Body)
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("root[0] children = %d, want 1", len(tree[0].Children))
	}
	if tree[0].Children[0].Body != "reply" {
		t.Errorf("child body = %q", tree[0].Children[0].Body)
	}
	// Flat view supplies parent IDs directly — confirm the reply links back.
	if tree[0].Children[0].ParentID != 1000 {
		t.Errorf("child parent_id = %d, want 1000", tree[0].Children[0].ParentID)
	}
	// Pin the Level and DateUnix mapping from Site.page JSON so a regression in
	// fetchCommentsPage's field mapping is caught.
	if tree[0].Level != 1 {
		t.Errorf("root level = %d, want 1", tree[0].Level)
	}
	if tree[0].Children[0].Level != 2 {
		t.Errorf("child level = %d, want 2", tree[0].Children[0].Level)
	}
	if tree[0].DateUnix != 1577836800 {
		t.Errorf("root date_unix = %d, want 1577836800", tree[0].DateUnix)
	}
	if tree[1].Body != "page two comment" {
		t.Errorf("root[1] body = %q", tree[1].Body)
	}
}

// TestParseCommentsDeduplicatesOverfetch simulates LJ's 'returns last page
// forever' quirk: the server claims maxPage=3 but returns the same payload
// for every page request. Without dedupe in BuildCommentTree the tree would
// have triple-counted comments. The test pins the idempotency contract.
func TestParseCommentsDeduplicatesOverfetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return page 1 payload, regardless of which page was requested.
		fmt.Fprint(w, testCommentsHTML(1, 3))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	tree, err := ParseComments(context.Background(), client, "testuser", 12345)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("roots = %d, want 1 (duplicates collapsed)", len(tree))
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("children = %d, want 1 (duplicates collapsed)", len(tree[0].Children))
	}
}

func TestClientGet404(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	client := newTestClient(srv.URL)
	_, err := client.Get(context.Background(), srv.URL+"/nope")
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestClientGetCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Get(ctx, srv.URL+"/")
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// --- Retry tests ---

func TestGetRetryOn500(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n <= 2 {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	resp, err := client.Get(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	resp.Body.Close()
	if got := count.Load(); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

func TestGetNoRetryOn404(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(404)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	_, err := client.Get(context.Background(), srv.URL+"/nope")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := count.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestGetRetryExhausted(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	_, err := client.Get(context.Background(), srv.URL+"/")
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if got := count.Load(); got != int32(maxRetries) {
		t.Errorf("requests = %d, want %d", got, maxRetries)
	}
}

func TestGetRetryCanceledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.retryBackoff = 10 * time.Second // long backoff so we can cancel during it

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := client.Get(ctx, srv.URL+"/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, should have been canceled quickly", elapsed)
	}
}

// --- Image download tests ---

func TestDownloadImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("FAKE_IMAGE_DATA"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := fmt.Sprintf(`<p>text</p><img src="%s/photo.jpg"><img src="%s/pic.png">`, srv.URL, srv.URL)
	result := downloadImages(context.Background(), client, html)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("downloaded %d files, want 2", len(entries))
	}

	if strings.Contains(result, srv.URL) {
		t.Errorf("result still contains server URL: %s", result)
	}
	// HTML always uses forward slashes; on Windows TempDir gives backslashes.
	wantDir := filepath.ToSlash(dir)
	if !strings.Contains(result, wantDir) {
		t.Errorf("result missing local dir %s: %s", wantDir, result)
	}
}

func TestDownloadImagesContentTypeFixesExtension(t *testing.T) {
	// Extension-less URL: server says it's a PNG, so the file (and the rewritten
	// src) should end in .png, not the default .jpg fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("FAKE_PNG"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := fmt.Sprintf(`<img src="%s/image?id=42">`, srv.URL)
	result := downloadImages(context.Background(), client, html)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("downloaded %d files, want 1", len(entries))
	}
	if filepath.Ext(entries[0].Name()) != ".png" {
		t.Errorf("saved as %s, want .png from Content-Type", entries[0].Name())
	}
	if !strings.Contains(result, `.png"`) {
		t.Errorf("src not rewritten to .png: %s", result)
	}
}

func TestDownloadImagesSkipsDataURI(t *testing.T) {
	// data: URIs would otherwise burn maxRetries x exponential backoff against
	// an unsupported scheme. Test that they're filtered out before any GET.
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Write([]byte("real"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := `<img src="data:image/png;base64,iVBORw0KGgo=">` +
		`<img src="javascript:alert(1)">` +
		fmt.Sprintf(`<img src="%s/real.jpg">`, srv.URL)
	result := downloadImages(context.Background(), client, html)

	if got := requestCount.Load(); got != 1 {
		t.Errorf("made %d HTTP requests, want 1 (only real.jpg)", got)
	}
	// data: and javascript: URLs must remain unchanged (we don't touch them).
	if !strings.Contains(result, "data:image/png;base64") {
		t.Errorf("data: URI was unexpectedly stripped: %s", result)
	}
	if !strings.Contains(result, "javascript:alert(1)") {
		t.Errorf("javascript: URL was unexpectedly stripped: %s", result)
	}
}

func TestDownloadImagesDedupsDuplicateSrc(t *testing.T) {
	// Two <img src="X"> in one post must fetch X once, not race on the same
	// temp file. Both <img> tags must end up rewritten to the same local path.
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("FAKE"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := fmt.Sprintf(`<img src="%s/dup.jpg"><p>x</p><img src="%s/dup.jpg">`, srv.URL, srv.URL)
	result := downloadImages(context.Background(), client, html)

	if got := requestCount.Load(); got != 1 {
		t.Errorf("made %d HTTP requests, want 1 (duplicate src deduped)", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("downloaded %d files, want 1", len(entries))
	}
	// Both <img> tags should have been rewritten — count occurrences of the
	// local path; the original server URL must be gone.
	if strings.Contains(result, srv.URL) {
		t.Errorf("server URL still present after rewrite: %s", result)
	}
	if c := strings.Count(result, `src="`+filepath.ToSlash(dir)); c != 2 {
		t.Errorf("expected both <img> rewritten to local path, found %d (result: %s)", c, result)
	}
}

func TestDownloadImagesSkipsOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := fmt.Sprintf(`<img src="%s/missing.jpg">`, srv.URL)
	result := downloadImages(context.Background(), client, html)

	// Image download failed — src should remain unchanged
	if !strings.Contains(result, srv.URL) {
		t.Errorf("expected original URL preserved on error, got: %s", result)
	}
}

func TestDownloadImagesProtocolRelative(t *testing.T) {
	// //host/pic.jpg must be fetched over https, not silently skipped. Needs a
	// TLS server because the URL is normalized to https://.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("FAKE_IMAGE"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := &Client{http: srv.Client(), baseURL: srv.URL, retryBackoff: time.Millisecond, HTTPConcurrency: 4, ImagesDir: dir}

	host := strings.TrimPrefix(srv.URL, "https://")
	html := fmt.Sprintf(`<img src="//%s/pic.jpg">`, host)
	result := downloadImages(context.Background(), client, html)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("downloaded %d files, want 1 (protocol-relative URL must be fetched)", len(entries))
	}
	if strings.Contains(result, "//"+host) {
		t.Errorf("protocol-relative src was not rewritten: %s", result)
	}
}

func TestDownloadImagesReusesRenamedExtension(t *testing.T) {
	// Extension-less URL saved as its real (PNG) extension on the first run. A
	// second run must reuse that file, not re-download because the skip-check
	// only stats the guessed .jpg name.
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("FAKE_PNG"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	client := newTestClient(srv.URL)
	client.ImagesDir = dir

	html := fmt.Sprintf(`<img src="%s/image?id=42">`, srv.URL)
	downloadImages(context.Background(), client, html) // downloads, renames .jpg -> .png
	downloadImages(context.Background(), client, html) // must reuse the .png

	if got := n.Load(); got != 1 {
		t.Errorf("downloads = %d, want 1 (second run must reuse the renamed file)", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files on disk = %d, want 1", len(entries))
	}
}

func TestParsePostBodyFormats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, testPostHTML)
	}))
	defer srv.Close()

	cases := []struct {
		format       string
		wantContains string
	}{
		{FormatText, "Hello world"},
		{FormatMarkdown, "Hello world"},
		{FormatHTML, "<p>Hello world</p>"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			client := newTestClient(srv.URL)
			client.baseURL = srv.URL + "/%s"
			client.BodyFormat = tc.format
			post, err := ParsePost(context.Background(), client, "testuser", 1)
			if err != nil {
				t.Fatalf("ParsePost: %v", err)
			}
			if !strings.Contains(post.Body, tc.wantContains) {
				t.Errorf("format %s: body = %q, want to contain %q", tc.format, post.Body, tc.wantContains)
			}
		})
	}
}

func TestParseCommentsBodyFormat(t *testing.T) {
	// Comment bodies must honour BodyFormat too, not stay raw HTML.
	comment := `{"article":"<b>bold</b> reply","uname":"u","dname":"U","talkid":1,"dtalkid":10,"parent":0,"level":1,"ctime":"","ctime_ts":0,"deleted":0}`
	page := fmt.Sprintf(`<html><body><script>Site.page = {"replycount":1,"comments":[%s]};</script></body></html>`, comment)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"
	client.BodyFormat = FormatMarkdown

	tree, err := ParseComments(context.Background(), client, "u", 1)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("roots = %d, want 1", len(tree))
	}
	if tree[0].Body != "**bold** reply" {
		t.Errorf("comment body = %q, want %q", tree[0].Body, "**bold** reply")
	}
}

// --- FindFirstPostID tests ---

func TestFindFirstPostID(t *testing.T) {
	// Simulate journal where posts exist at IDs >= 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		// Extract ID from path like /testuser/NNN.html
		base := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/testuser/"), ".html")
		id, _ := strconv.Atoi(base)
		if id >= 200 {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	id, err := FindFirstPostID(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FindFirstPostID: %v", err)
	}
	// Should find 200 via binary search between 128 and 256
	if id != 200 {
		t.Errorf("first post = %d, want 200", id)
	}
}

func TestFindFirstPostIDNoPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	_, err := FindFirstPostID(context.Background(), client, "testuser")
	if err == nil {
		t.Error("expected error for empty journal")
	}
}

// --- Journal tests ---

func TestParseJournal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		// Index pages (have skip param, no .html in path)
		if query.Get("skip") != "" {
			skip := query.Get("skip")
			if skip == "0" {
				fmt.Fprint(w, `<html><body>
					<a href="/111.html">Post 1</a>
					<a href="/222.html">Post 2</a>
				</body></html>`)
				return
			}
			fmt.Fprint(w, `<html><body></body></html>`)
			return
		}

		// Comment pages
		if query.Get("format") == "light" {
			fmt.Fprint(w, testCommentsHTML(1, 1))
			return
		}

		// Post pages
		fmt.Fprint(w, testPostHTML)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	var posts []*Post
	err := ParseJournal(context.Background(), client, "testuser", false, func(p *Post) error {
		posts = append(posts, p)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseJournal: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2", len(posts))
	}
	if posts[0].ID != 111 {
		t.Errorf("posts[0].ID = %d, want 111", posts[0].ID)
	}
	if posts[1].ID != 222 {
		t.Errorf("posts[1].ID = %d, want 222", posts[1].ID)
	}
	if posts[0].Title != "Test Post Title" {
		t.Errorf("posts[0].Title = %q", posts[0].Title)
	}
}

func TestParseJournalWithComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		if query.Get("skip") != "" {
			if query.Get("skip") == "0" {
				fmt.Fprint(w, `<html><body><a href="/111.html">Post</a></body></html>`)
				return
			}
			fmt.Fprint(w, `<html><body></body></html>`)
			return
		}

		if query.Get("format") == "light" {
			fmt.Fprint(w, testCommentsHTML(1, 1))
			return
		}

		fmt.Fprint(w, testPostHTML)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	var posts []*Post
	err := ParseJournal(context.Background(), client, "testuser", true, func(p *Post) error {
		posts = append(posts, p)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseJournal: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	if len(posts[0].Comments) != 1 {
		t.Errorf("comment roots = %d, want 1", len(posts[0].Comments))
	}
}

func TestFetchPostIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("skip") != "" {
			skip := query.Get("skip")
			if skip == "0" {
				// Newest first (LJ order)
				fmt.Fprint(w, `<html><body>
					<a href="/333.html">Post 3</a>
					<a href="/222.html">Post 2</a>
					<a href="/111.html">Post 1</a>
				</body></html>`)
				return
			}
			fmt.Fprint(w, `<html><body></body></html>`)
			return
		}
		fmt.Fprint(w, testPostHTML)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	index, err := FetchPostIndex(context.Background(), client, "testuser")
	if err != nil {
		t.Fatalf("FetchPostIndex: %v", err)
	}
	// Should be reversed to chronological: 111, 222, 333
	if len(index) != 3 {
		t.Fatalf("index = %v, want 3 elements", index)
	}
	if index[0] != 111 || index[1] != 222 || index[2] != 333 {
		t.Errorf("index = %v, want [111 222 333]", index)
	}
}
