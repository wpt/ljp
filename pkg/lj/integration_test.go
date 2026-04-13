package lj

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

const testPostHTML = `<!DOCTYPE html>
<html><head>
<meta property="og:title" content="Test Post"/>
<meta property="og:description" content="A test"/>
<meta property="og:image" content="http://example.com/img.jpg"/>
</head><body>
<h1 class="aentry-post__title">Test Post Title</h1>
<time>January 15 2020, 10:30</time>
<div class="aentry-post__text aentry-post__text--view">
<p>Hello world</p>
</div>
<a href="/tag/test">test</a>
<a href="/tag/go">go</a>
</body></html>`

func testCommentsHTML(page, maxPage int) string {
	var comments string
	switch page {
	case 1:
		comments = `{"article":"first comment","uname":"user1","dname":"User One","talkid":100,"dtalkid":1000,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0,"loaded":1,"thread":1000},{"article":"reply","uname":"user2","dname":"User Two","talkid":101,"dtalkid":1001,"parent":1000,"level":2,"ctime":"January 1 2020, 13:00:00 UTC","ctime_ts":1577840400,"subject":"re","userpic":"","deleted":0,"loaded":1,"thread":1001}`
	case 2:
		comments = `{"article":"page two comment","uname":"user3","dname":"User Three","talkid":200,"dtalkid":2000,"parent":0,"level":1,"ctime":"January 2 2020, 10:00:00 UTC","ctime_ts":1577959200,"subject":"","userpic":"","deleted":0,"loaded":1,"thread":2000}`
	default:
		comments = ""
	}

	var pageLinks string
	for i := 1; i <= maxPage; i++ {
		pageLinks += fmt.Sprintf(`<a href="?page=%d&format=light">%d</a> `, i, i)
	}

	return fmt.Sprintf(`<html><body>%s<script>Site.page = {"replycount":3,"comments":[%s]};</script></body></html>`, pageLinks, comments)
}

// newTestClient creates a client pointing at the test server.
func newTestClient(serverURL string) *Client {
	return &Client{
		http:         http.DefaultClient,
		limiter:      rate.NewLimiter(rate.Inf, 1),
		baseURL:      serverURL,
		retryBackoff: time.Millisecond,
	}
}

func setupTestServer(maxPage int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		query := r.URL.Query()

		if query.Get("format") == "light" {
			page := 1
			if p := query.Get("page"); p != "" {
				fmt.Sscanf(p, "%d", &page)
			}
			fmt.Fprint(w, testCommentsHTML(page, maxPage))
			return
		}

		if path == "/12345.html" {
			fmt.Fprint(w, testPostHTML)
			return
		}

		http.NotFound(w, r)
	}))
}

func TestParsePostFull(t *testing.T) {
	srv := setupTestServer(1)
	defer srv.Close()

	client := newTestClient(srv.URL)
	// baseURL has no %s, so user param is ignored in URL building
	// postURL will produce: http://127.0.0.1:PORT/12345.html (with "testuser" ignored since no %s)
	// We need baseURL to include %s pattern. Let's use a fixed approach instead.
	client.baseURL = srv.URL + "/%s" // will produce srv.URL + "/testuser" but server ignores path prefix

	// Actually simpler: make the server match any .html path
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("format") == "light" {
			page := 1
			if p := query.Get("page"); p != "" {
				fmt.Sscanf(p, "%d", &page)
			}
			fmt.Fprint(w, testCommentsHTML(page, 2))
			return
		}
		fmt.Fprint(w, testPostHTML)
	}))
	defer srv2.Close()

	client2 := newTestClient(srv2.URL)
	// baseURL format: "http://host/%s" so postURL("user", 12345) => "http://host/user/12345.html"
	// But our server doesn't care about the path, it serves testPostHTML for everything non-light
	client2.baseURL = srv2.URL + "/%s"

	ctx := context.Background()
	post, err := ParsePost(ctx, client2, "testuser", 12345)
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

// testFlatCommentsHTML returns comments in flat view (all bodies, parent=0).
func testFlatCommentsHTML(page, maxPage int) string {
	var comments string
	switch page {
	case 1:
		comments = `{"article":"first comment","uname":"user1","dname":"User One","talkid":100,"dtalkid":1000,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0,"loaded":1,"thread":1000},{"article":"reply","uname":"user2","dname":"User Two","talkid":101,"dtalkid":1001,"parent":0,"level":1,"ctime":"January 1 2020, 13:00:00 UTC","ctime_ts":1577840400,"subject":"re","userpic":"","deleted":0,"loaded":1,"thread":1001}`
	default:
		comments = ""
	}
	var pageLinks string
	for i := 1; i <= maxPage; i++ {
		pageLinks += fmt.Sprintf(`<a href="?page=%d&format=light">%d</a> `, i, i)
	}
	return fmt.Sprintf(`<html><body>%s<script>Site.page = {"replycount":2,"comments":[%s]};</script></body></html>`, pageLinks, comments)
}

// testThreadedCommentsHTML returns comments in threaded view (parent info, some loaded:0).
func testThreadedCommentsHTML(page, maxPage int) string {
	var comments string
	switch page {
	case 1:
		comments = `{"article":"first comment","uname":"user1","dname":"User One","talkid":100,"dtalkid":1000,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0,"loaded":1,"thread":1000},{"article":"","uname":"user2","dname":"User Two","talkid":101,"dtalkid":1001,"parent":1000,"level":2,"ctime":"January 1 2020, 13:00:00 UTC","ctime_ts":1577840400,"subject":"re","userpic":"","deleted":0,"loaded":0,"thread":1001}`
	default:
		comments = ""
	}
	var pageLinks string
	for i := 1; i <= maxPage; i++ {
		pageLinks += fmt.Sprintf(`<a href="?page=%d">%d</a> `, i, i)
	}
	return fmt.Sprintf(`<html><body>%s<script>Site.page = {"replycount":2,"comments":[%s]};</script></body></html>`, pageLinks, comments)
}

func TestParseCommentsFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
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
	if tree[1].Body != "page two comment" {
		t.Errorf("root[1] body = %q", tree[1].Body)
	}
}

func TestParseCommentsDualView(t *testing.T) {
	// Flat view: bodies present, parent=0 for all
	// Threaded view: parent info present, some loaded:0
	// Result: bodies from flat, tree from threaded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		page := 1
		if p := query.Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}

		if query.Get("view") == "flat" {
			fmt.Fprint(w, testFlatCommentsHTML(page, 1))
		} else {
			fmt.Fprint(w, testThreadedCommentsHTML(page, 1))
		}
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	tree, err := ParseComments(context.Background(), client, "testuser", 12345)
	if err != nil {
		t.Fatalf("ParseComments: %v", err)
	}

	// Root comment should have body from flat + child from threaded parent mapping
	if len(tree) != 1 {
		t.Fatalf("roots = %d, want 1 (second is child of first)", len(tree))
	}
	if tree[0].Body != "first comment" {
		t.Errorf("root body = %q, want 'first comment'", tree[0].Body)
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("root children = %d, want 1", len(tree[0].Children))
	}
	if tree[0].Children[0].Body != "reply" {
		t.Errorf("child body = %q, want 'reply'", tree[0].Children[0].Body)
	}
	if tree[0].Children[0].ParentID != 1000 {
		t.Errorf("child parent_id = %d, want 1000", tree[0].Children[0].ParentID)
	}
}

func TestParseCommentsThreadedFails(t *testing.T) {
	// Flat view works, threaded returns 500 — should still return flat comments
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		page := 1
		if p := query.Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}

		if query.Get("view") == "flat" {
			fmt.Fprint(w, testFlatCommentsHTML(page, 1))
		} else {
			// Threaded view fails
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	client.baseURL = srv.URL + "/%s"

	var warnings []string
	client.Log = func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	tree, err := ParseComments(context.Background(), client, "testuser", 12345)
	if err != nil {
		t.Fatalf("ParseComments should not fail: %v", err)
	}

	// Should return flat comments (all as roots since no parent info)
	if len(tree) != 2 {
		t.Fatalf("roots = %d, want 2 (flat, no tree)", len(tree))
	}

	// Should have logged a warning
	hasWarning := false
	for _, w := range warnings {
		if strings.Contains(w, "threaded view") {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Errorf("expected warning about threaded view failure, got: %v", warnings)
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
		t.Error("expected error for canceled context")
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

	// Check images were downloaded
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("downloaded %d files, want 2", len(entries))
	}

	// Check src attributes were rewritten to local paths
	if strings.Contains(result, srv.URL) {
		t.Errorf("result still contains server URL: %s", result)
	}
	if !strings.Contains(result, dir) {
		t.Errorf("result doesn't contain local dir %s: %s", dir, result)
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

// --- FindFirstPostID tests ---

func TestFindFirstPostID(t *testing.T) {
	// Simulate journal where posts exist at IDs >= 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		// Extract ID from path like /testuser/NNN.html
		path := r.URL.Path
		var id int
		fmt.Sscanf(path, "/testuser/%d.html", &id)
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
