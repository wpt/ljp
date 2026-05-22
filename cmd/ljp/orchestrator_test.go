package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wpt/ljp/pkg/lj"
)

// fakeLJServer stands in for LiveJournal. Posts and the journal index are
// supplied via the constructor; comments are derived from a per-id map. Used
// by every orchestrator test below.
type fakeLJServer struct {
	postIDs      []int       // chronological order (oldest first)
	commentsByID map[int]int // id -> number of comments to synthesize
	httpServer   *httptest.Server
}

func newFakeLJServer(postIDs []int, commentsByID map[int]int) *fakeLJServer {
	if commentsByID == nil {
		commentsByID = map[int]int{}
	}
	f := &fakeLJServer{postIDs: postIDs, commentsByID: commentsByID}
	f.httpServer = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeLJServer) Close()                  { f.httpServer.Close() }
func (f *fakeLJServer) baseURLTemplate() string { return f.httpServer.URL + "/%s" }

func (f *fakeLJServer) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// ?skip=N — index page in newest-first order (LJ ordering).
	if skip := q.Get("skip"); skip != "" {
		skipN, _ := strconv.Atoi(skip)
		// Newest first: reverse postIDs.
		newest := make([]int, len(f.postIDs))
		for i, id := range f.postIDs {
			newest[len(f.postIDs)-1-i] = id
		}
		if skipN >= len(newest) {
			fmt.Fprint(w, `<html><body></body></html>`)
			return
		}
		end := skipN + 20
		if end > len(newest) {
			end = len(newest)
		}
		fmt.Fprint(w, "<html><body>")
		for _, id := range newest[skipN:end] {
			fmt.Fprintf(w, `<a href="/%d.html">post %d</a>`, id, id)
		}
		fmt.Fprint(w, "</body></html>")
		return
	}

	// /YYYY/MM/ monthly archive (FetchFullPostIndex). For test simplicity
	// every month returns the full post list; BuildCommentTree/dedupe in
	// FetchFullPostIndex collapses the duplicates so the final index is the
	// same as supplying it once.
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 {
		fmt.Fprint(w, "<html><body>")
		for _, id := range f.postIDs {
			fmt.Fprintf(w, `<a href="/%d.html">post %d</a>`, id, id)
		}
		fmt.Fprint(w, "</body></html>")
		return
	}

	// Comment page (?format=light).
	if q.Get("format") == "light" {
		idStr := strings.TrimSuffix(filepath.Base(r.URL.Path), ".html")
		id, _ := strconv.Atoi(idStr)
		n := f.commentsByID[id]
		var items []string
		for i := 1; i <= n; i++ {
			items = append(items, fmt.Sprintf(
				`{"article":"comment %d","uname":"u%d","dname":"User %d","talkid":%d,"dtalkid":%d,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0}`,
				i, i, i, 100+i, 1000+i))
		}
		fmt.Fprintf(w, `<html><body><script>Site.page = {"replycount":%d,"comments":[%s]};</script></body></html>`,
			n, strings.Join(items, ","))
		return
	}

	// Post page.
	idStr := strings.TrimSuffix(filepath.Base(r.URL.Path), ".html")
	id, _ := strconv.Atoi(idStr)
	fmt.Fprintf(w, `<html><head>
<meta property="og:title" content="post %d"/>
</head><body>
<h1 class="aentry-post__title">Post %d</h1>
<time>January 1 2020, 12:00</time>
<div class="aentry-post__text aentry-post__text--view">
<p>Body of %d</p>
</div>
</body></html>`, id, id, id)
}

func newOrchestratorClient(srv *fakeLJServer) *lj.Client {
	c := lj.NewClient()
	c.SetBaseURL(srv.baseURLTemplate())
	c.SetConcurrency(2)
	return c
}

// captureStderr redirects os.Stderr for the duration of fn so the orchestrator
// test output stays clean.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestRunParallel(t *testing.T) {
	srv := newFakeLJServer([]int{1, 2, 3}, map[int]int{1: 0, 2: 1, 3: 2})
	defer srv.Close()

	dir := t.TempDir()
	client := newOrchestratorClient(srv)

	stderr := captureStderr(t, func() {
		runParallel(context.Background(), client, "testuser", []int{1, 2, 3}, true, dir, false, false)
	})

	entries, _ := os.ReadDir(dir)
	jsonCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != 3 {
		t.Errorf("got %d .json files, want 3 (stderr: %s)", jsonCount, stderr)
	}
	if !strings.Contains(stderr, "3/3") {
		t.Errorf("expected progress 3/3 in stderr, got: %s", stderr)
	}
}

func TestRunLatestWithComments(t *testing.T) {
	// 5 posts; only ids 3 and 5 have comments; ask for 2 newest with comments.
	srv := newFakeLJServer([]int{1, 2, 3, 4, 5}, map[int]int{3: 1, 5: 2})
	defer srv.Close()

	dir := t.TempDir()
	client := newOrchestratorClient(srv)

	stderr := captureStderr(t, func() {
		runLatestWithComments(context.Background(), client, "testuser", 2, dir, false, false)
	})

	entries, _ := os.ReadDir(dir)
	got := map[int]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			got[id] = true
		}
	}
	// Expect post 5 (2 comments) and post 3 (1 comment). Posts 1/2/4 have zero
	// comments so they're skipped without consuming a slot.
	if !got[3] || !got[5] {
		t.Errorf("got %v, want {3, 5} (stderr: %s)", got, stderr)
	}
	if got[1] || got[2] || got[4] {
		t.Errorf("zero-comment posts shouldn't have been written: %v", got)
	}
}

func TestRunJournalMode(t *testing.T) {
	srv := newFakeLJServer([]int{1, 2}, nil)
	defer srv.Close()

	dir := t.TempDir()
	client := newOrchestratorClient(srv)

	stderr := captureStderr(t, func() {
		runJournalMode(context.Background(), client, "testuser", false, dir, false, false)
	})

	entries, _ := os.ReadDir(dir)
	got := map[int]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		got[id] = true
	}
	if !got[1] || !got[2] || len(got) != 2 {
		t.Errorf("got %v, want {1, 2} (stderr: %s)", got, stderr)
	}
}

func TestRunSelectionMode_OrdinalRange(t *testing.T) {
	srv := newFakeLJServer([]int{10, 20, 30, 40, 50}, nil)
	defer srv.Close()

	dir := t.TempDir()
	client := newOrchestratorClient(srv)

	sel, err := parseSelector("2-4")
	if err != nil {
		t.Fatalf("parseSelector: %v", err)
	}

	stderr := captureStderr(t, func() {
		runSelectionMode(context.Background(), client, "testuser", sel, false, dir, false, false)
	})

	// Ordinal 1 = oldest. After FetchPostIndex reverses LJ's newest-first,
	// chronological order is [10, 20, 30, 40, 50]. So 2-4 should select ids 20, 30, 40.
	entries, _ := os.ReadDir(dir)
	got := map[int]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		got[id] = true
	}
	for _, want := range []int{20, 30, 40} {
		if !got[want] {
			t.Errorf("missing post %d (got %v, stderr: %s)", want, got, stderr)
		}
	}
	// Ordinals outside 2-4 must NOT be in the output.
	if got[10] || got[50] {
		t.Errorf("posts outside range 2-4 should not be written, got %v", got)
	}
}

func TestRunSelectionMode_LJIDList(t *testing.T) {
	srv := newFakeLJServer([]int{100, 200, 300}, nil)
	defer srv.Close()

	dir := t.TempDir()
	client := newOrchestratorClient(srv)

	sel, err := parseSelector("@100,@300")
	if err != nil {
		t.Fatalf("parseSelector: %v", err)
	}

	captureStderr(t, func() {
		runSelectionMode(context.Background(), client, "testuser", sel, false, dir, false, false)
	})

	entries, _ := os.ReadDir(dir)
	got := map[int]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		got[id] = true
	}
	if !got[100] || !got[300] {
		t.Errorf("got %v, want {100, 300}", got)
	}
	if got[200] {
		t.Errorf("post 200 should not have been fetched: %v", got)
	}
}

func TestFetchAndWrite_PostFailureIsWarning(t *testing.T) {
	// Server that 404s every post fetch — fetchAndWrite must log a warning
	// and return nil (not propagate as fatal).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := lj.NewClient()
	client.SetBaseURL(srv.URL + "/%s")
	client.SetConcurrency(2)

	dir := t.TempDir()
	writer, err := makePostWriter(dir, false, false)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}

	captureStderr(t, func() {
		err = fetchAndWrite(context.Background(), client, "testuser", 42, false, false, writer)
	})
	if err != nil {
		t.Errorf("fetchAndWrite returned error %v, want nil (404 should be a warning)", err)
	}
	// No file should have been written.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("no files should be written on fetch failure, got %d", len(entries))
	}
}

func TestFetchAndWriteCancelledIsFatal(t *testing.T) {
	// A cancelled context (SIGINT) must propagate as an error so the orchestrator
	// exits non-zero (130), not get swallowed as a per-post warning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := lj.NewClient()
	client.SetBaseURL(srv.URL + "/%s")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	writer, err := makePostWriter(t.TempDir(), false, false)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}
	captureStderr(t, func() {
		err = fetchAndWrite(ctx, client, "testuser", 42, false, false, writer)
	})
	if err == nil {
		t.Error("fetchAndWrite swallowed a cancelled context; want error so the run exits 130")
	}
}

func TestFetchAndWriteFatalOnError(t *testing.T) {
	// With fatalOnError set (a single explicitly-named post), a 404 must be an
	// error, not a warning+nil — so `ljp news @missing` exits non-zero.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	client := lj.NewClient()
	client.SetBaseURL(srv.URL + "/%s")

	writer, err := makePostWriter(t.TempDir(), false, false)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}
	captureStderr(t, func() {
		err = fetchAndWrite(context.Background(), client, "testuser", 42, false, true, writer)
	})
	if err == nil {
		t.Error("fetchAndWrite returned nil on 404 with fatalOnError set; want error")
	}
}

func TestResolveLJIDs_OrdinalRangeOutOfBounds(t *testing.T) {
	srv := newFakeLJServer([]int{1, 2, 3}, nil)
	defer srv.Close()
	client := newOrchestratorClient(srv)

	sel, _ := parseSelector("10-20")
	captureStderr(t, func() {
		ids, err := resolveLJIDs(context.Background(), client, "testuser", sel)
		if err == nil {
			t.Errorf("expected error for out-of-range start ordinal, got ids=%v", ids)
		}
	})
}

func TestResolveLJIDs_OrdinalRangeCapped(t *testing.T) {
	srv := newFakeLJServer([]int{1, 2, 3}, nil)
	defer srv.Close()
	client := newOrchestratorClient(srv)

	sel, _ := parseSelector("1-100")
	var ids []int
	var err error
	captureStderr(t, func() {
		ids, err = resolveLJIDs(context.Background(), client, "testuser", sel)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(ids, []int{1, 2, 3}) {
		t.Errorf("got ids=%v, want [1 2 3] (chronological, capped to journal length)", ids)
	}
}

func TestResolveLJIDs_LJIDRange(t *testing.T) {
	// Journal has posts 100, 250, 999. Range @200-@800 should match 250 only.
	srv := newFakeLJServer([]int{100, 250, 999}, nil)
	defer srv.Close()
	client := newOrchestratorClient(srv)

	sel, _ := parseSelector("@200-@800")
	var ids []int
	captureStderr(t, func() {
		ids, _ = resolveLJIDs(context.Background(), client, "testuser", sel)
	})
	if len(ids) != 1 || ids[0] != 250 {
		t.Errorf("got ids=%v, want [250]", ids)
	}
}

// quietJSONUnmarshal is a tiny convenience for asserting written posts.
func quietJSONUnmarshal(t *testing.T, path string) lj.Post {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var p lj.Post
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return p
}

func TestRunSelectionMode_SingleLJID(t *testing.T) {
	// Single-ID case takes the sequential fetchAndWrite path, not runParallel.
	srv := newFakeLJServer([]int{77}, nil)
	defer srv.Close()
	client := newOrchestratorClient(srv)

	sel, _ := parseSelector("@77")
	dir := t.TempDir()
	captureStderr(t, func() {
		runSelectionMode(context.Background(), client, "testuser", sel, false, dir, false, false)
	})

	p := quietJSONUnmarshal(t, filepath.Join(dir, "77.json"))
	if p.ID != 77 {
		t.Errorf("got id %d, want 77", p.ID)
	}
}
