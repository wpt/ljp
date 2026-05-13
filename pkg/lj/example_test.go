package lj_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/wpt/ljp/pkg/lj"
)

// fakePost is a minimal LJ-shaped post page used by the runnable examples
// below. Keeps godoc snippets self-contained without network access.
const fakePost = `<!DOCTYPE html>
<html><head>
<meta property="og:title" content="Example"/>
</head><body>
<h1 class="aentry-post__title">Example post</h1>
<time>January 1 2020, 12:00</time>
<div class="aentry-post__text aentry-post__text--view">
<p>Hello world</p>
</div>
</body></html>`

const fakeComments = `<html><body>
<script>Site.page = {"replycount":1,"comments":[
{"article":"hi","uname":"u1","dname":"User One","talkid":100,"dtalkid":1000,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0}
]};</script>
</body></html>`

// Example_parsePost demonstrates the minimal flow: construct a Client,
// fetch a single post.
func Example_parsePost() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, fakePost)
	}))
	defer srv.Close()

	client := lj.NewClient()
	client.SetBaseURL(srv.URL + "/%s")

	post, err := lj.ParsePost(context.Background(), client, "news", 166511)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(post.Title)
	// Output: Example post
}

// Example_parseComments shows how to fetch the comment tree and walk it.
// CountComments returns the total number across all nested children.
func Example_parseComments() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "light" {
			fmt.Fprint(w, fakeComments)
			return
		}
		fmt.Fprint(w, fakePost)
	}))
	defer srv.Close()

	client := lj.NewClient()
	client.SetBaseURL(srv.URL + "/%s")

	tree, err := lj.ParseComments(context.Background(), client, "news", 166511)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("total:", lj.CountComments(tree))
	// Output: total: 1
}

// Example_buildCommentTree shows the flat→tree transformation in isolation.
// Useful if you've stored the flat comments yourself and only want the
// nesting logic.
func Example_buildCommentTree() {
	flat := []*lj.Comment{
		{ID: 1, Body: "root"},
		{ID: 2, ParentID: 1, Body: "reply"},
		{ID: 3, ParentID: 2, Body: "nested"},
	}
	tree := lj.BuildCommentTree(flat)
	fmt.Println("roots:", len(tree))
	fmt.Println("root.children:", len(tree[0].Children))
	fmt.Println("total:", lj.CountComments(tree))
	// Output:
	// roots: 1
	// root.children: 1
	// total: 3
}
