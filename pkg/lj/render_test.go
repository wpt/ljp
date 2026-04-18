package lj

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderPost_SanitizesScriptInBody(t *testing.T) {
	post := &Post{
		ID:    1,
		Title: "hi",
		Body:  `<p>hello</p><script>alert('xss')</script><img src="x" onerror="alert(1)">`,
	}
	var buf bytes.Buffer
	if err := RenderPost(&buf, post); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>") || strings.Contains(out, "alert(") {
		t.Errorf("script tag not sanitized: %s", out)
	}
	if strings.Contains(out, "onerror") {
		t.Errorf("event handler attr not stripped: %s", out)
	}
	if !strings.Contains(out, "<p>hello</p>") {
		t.Errorf("benign body content was stripped: %s", out)
	}
}

func TestRenderPost_SanitizesScriptInComment(t *testing.T) {
	post := &Post{
		ID:    1,
		Title: "hi",
		Body:  "body",
		Comments: []*Comment{
			{ID: 10, Author: "bad", Body: `<a href="javascript:alert(1)">click</a>`},
			{ID: 11, Author: "ok", Body: "<em>fine</em>"},
		},
	}
	var buf bytes.Buffer
	if err := RenderPost(&buf, post); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(strings.ToLower(out), "javascript:") {
		t.Errorf("javascript: URL not neutralized: %s", out)
	}
	if !strings.Contains(out, "<em>fine</em>") {
		t.Errorf("benign comment content was stripped: %s", out)
	}
}

func TestRenderPost_BlocksSvgDataURI(t *testing.T) {
	// SVG image data URLs can carry inline <script>, so they're rejected even
	// though raster data: URLs are allowed.
	post := &Post{
		ID:    1,
		Title: "hi",
		Body: `<img src="data:image/svg+xml;base64,PHN2Zz48c2NyaXB0PmFsZXJ0KDEpPC9zY3JpcHQ+PC9zdmc+">` +
			`<img src="data:image/png;base64,iVBORw0KGgo=">`,
	}
	var buf bytes.Buffer
	if err := RenderPost(&buf, post); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(strings.ToLower(out), "data:image/svg") {
		t.Errorf("SVG data: URL was not stripped: %s", out)
	}
	if !strings.Contains(out, "data:image/png") {
		t.Errorf("PNG data: URL should have been preserved: %s", out)
	}
}

func TestRenderPost_CommentCountIsTotal(t *testing.T) {
	post := &Post{
		ID:    1,
		Title: "hi",
		Comments: []*Comment{
			{ID: 1, Children: []*Comment{
				{ID: 2}, {ID: 3, Children: []*Comment{{ID: 4}}},
			}},
		},
	}
	var buf bytes.Buffer
	if err := RenderPost(&buf, post); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Comments (4)") {
		t.Errorf("expected total count 4, got: %s", buf.String())
	}
}

func TestRenderPost_TitleFallbackToID(t *testing.T) {
	post := &Post{ID: 42, Title: "", Body: "x"}
	var buf bytes.Buffer
	if err := RenderPost(&buf, post); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "<title>#42</title>") {
		t.Errorf("expected #42 title fallback, got: %s", buf.String())
	}
}
