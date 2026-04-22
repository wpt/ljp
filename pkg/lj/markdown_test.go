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
		{name: "paragraph", html: "<p>first</p><p>second</p>", want: "first\n\nsecond"},
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
		// Nested lists keep their structure via two-space indent per level.
		{name: "nested list", html: "<ul><li>a<ul><li>b</li></ul></li><li>c</li></ul>", want: "- a\n  - b\n- c"},
		// Adjacent block elements don't glue their text together.
		{name: "div per line", html: "<div>one</div><div>two</div>", want: "one\n\ntwo"},
		// Emphasis with boundary whitespace stays valid (markers hug the text).
		{name: "bold with inner spaces", html: "a<b> bold </b>b", want: "a **bold** b"},
		// Literal Markdown metacharacters in text are escaped.
		{name: "escape asterisks", html: "rate 3*4 and a_b", want: `rate 3\*4 and a\_b`},
		{name: "strikethrough", html: "<s>gone</s>", want: "~~gone~~"},
		{name: "heading h4", html: "<h4>Minor</h4>", want: "#### Minor"},
		{name: "inline code", html: "use <code>go test</code>", want: "use `go test`"},
		{name: "code block", html: "<pre>line1\nline2</pre>", want: "```\nline1\nline2\n```"},
		{name: "hr", html: "a<hr>b", want: "a\n\n---\n\nb"},
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

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{name: "plain", html: "hello world", want: "hello world"},
		// The core fix: words must not glue across block boundaries.
		{name: "paragraphs not glued", html: "<p>Привет</p><p>мир</p>", want: "Привет\n\nмир"},
		{name: "br is a newline", html: "line1<br>line2", want: "line1\nline2"},
		{name: "div per line", html: "<div>one</div><div>two</div>", want: "one\n\ntwo"},
		{name: "tags stripped", html: `<a href="x">link</a> and <b>bold</b>`, want: "link and bold"},
		{name: "no markdown escaping", html: "rate 3*4", want: "rate 3*4"},
		{name: "empty", html: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := htmlToText(tt.html); got != tt.want {
				t.Errorf("htmlToText(%q)\n  got  %q\n  want %q", tt.html, got, tt.want)
			}
		})
	}
}
