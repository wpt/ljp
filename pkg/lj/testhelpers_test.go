package lj

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Shared test helpers, fixtures, and clients. Lives in its own file so edits
// to integration_test.go (which is the largest test file) don't accidentally
// break unrelated tests in client_test.go, journal_test.go, parser_test.go.

const testPostHTML = `<!DOCTYPE html>
<html><head>
<meta property="og:title" content="Test Post"/>
<meta property="og:description" content="A test"/>
<meta property="og:image" content="http://example.com/img.jpg"/>
</head><body>
<article class="aentry-post">
<h1 class="aentry-post__title">Test Post Title</h1>
<time>January 15 2020, 10:30</time>
<div class="aentry-post__text aentry-post__text--view">
<p>Hello world</p>
</div>
<div class="aentry-post__tags">
<a href="/tag/test">test</a>
<a href="/tag/go">go</a>
</div>
</article>
<!-- sidebar tagcloud — must NOT bleed into Post.Tags -->
<aside class="sidebar"><a href="/tag/sidebar-noise">sidebar-noise</a></aside>
</body></html>`

// testCommentsHTML returns LJ-style flat view: fully loaded bodies AND parent
// IDs in a single fetch. Real LJ flat view supplies `parent` directly.
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

// newTestClient creates a client pointing at the test server with a 1ms
// retryBackoff so retry-path tests finish instantly.
func newTestClient(serverURL string) *Client {
	return &Client{
		http:            http.DefaultClient,
		baseURL:         serverURL,
		retryBackoff:    time.Millisecond,
		HTTPConcurrency: 4,
	}
}

// queryPage extracts the ?page=N query parameter; returns 1 if missing or invalid.
func queryPage(r *http.Request) int {
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return 1
}
