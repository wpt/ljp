package lj

import "testing"

func TestHTMLToMarkdown(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{name: "plain text", html: "hello world", want: "hello world"},
		{name: "br", html: "line1<br>line2", want: "line1\nline2"},
		{name: "br slash", html: "line1<br/>line2", want: "line1\nline2"},
		{name: "paragraph", html: "<p>first</p><p>second</p>", want: "first\n\n\n\nsecond"},
		{name: "bold", html: "<b>bold</b> text", want: "**bold** text"},
		{name: "strong", html: "<strong>bold</strong>", want: "**bold**"},
		{name: "italic", html: "<i>italic</i> text", want: "*italic* text"},
		{name: "em", html: "<em>italic</em>", want: "*italic*"},
		{name: "link", html: `<a href="https://example.com">click</a>`, want: "[click](https://example.com)"},
		{name: "image", html: `<img src="https://example.com/img.jpg"/>`, want: "![](https://example.com/img.jpg)"},
		{name: "image with alt", html: `<img src="x.jpg" alt="photo"/>`, want: "![photo](x.jpg)"},
		{name: "blockquote", html: "<blockquote>quoted text</blockquote>", want: "> quoted text"},
		{name: "unordered list", html: "<ul><li>one</li><li>two</li></ul>", want: "- one\n- two"},
		{name: "ordered list", html: "<ol><li>one</li><li>two</li></ol>", want: "1. one\n2. two"},
		{name: "heading h1", html: "<h1>Title</h1>", want: "# Title"},
		{name: "heading h2", html: "<h2>Sub</h2>", want: "## Sub"},
		{name: "heading h3", html: "<h3>Section</h3>", want: "### Section"},
		{name: "nested bold italic", html: "<b><i>both</i></b>", want: "***both***"},
		{name: "center passthrough", html: "<center>centered</center>", want: "centered"},
		{name: "unknown tag", html: "<div>content</div>", want: "content"},
		{name: "empty", html: "", want: ""},
		{name: "complex LJ", html: `text<br/><br/><i>«quoted text»</i><br/><br/><b>bold claim</b>`, want: "text\n\n*«quoted text»*\n\n**bold claim**"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTMLToMarkdown(tt.html)
			if got != tt.want {
				t.Errorf("HTMLToMarkdown(%q)\n  got  %q\n  want %q", tt.html, got, tt.want)
			}
		})
	}
}
