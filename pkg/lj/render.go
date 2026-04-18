package lj

import (
	"embed"
	"html/template"
	"io"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

//go:embed post.html
var templateFS embed.FS

var funcMap = template.FuncMap{
	// safeHTML lightly sanitises body HTML coming from LJ before bypassing
	// template auto-escape. Strips <script>/<iframe>/<style>/etc., on* event
	// handlers, and javascript:/vbscript: URLs in href/src — enough to avoid
	// surprise script execution if you open a downloaded post in a browser.
	"safeHTML":      func(s string) template.HTML { return template.HTML(sanitizeHTML(s)) },
	"countComments": CountComments,
}

var postTmpl = template.Must(
	template.New("post.html").Funcs(funcMap).ParseFS(templateFS, "post.html"),
)

// RenderPost writes post as an HTML page using the bundled post template.
// Post.Body and all Comment.Body strings are sanitized before injection.
func RenderPost(w io.Writer, post *Post) error {
	return postTmpl.Execute(w, post)
}

// dangerousTags are stripped wholesale (element + children). <form> is
// deliberately NOT here: forms don't execute script (the entire threat model is
// avoiding accidental script execution when opening a saved page), and stripping
// them wholesale destroyed visible content such as LJ polls. A form's action is
// still neutralised via urlBearingAttrs if it carries a javascript: URL.
var dangerousTags = map[string]bool{
	"script": true, "iframe": true, "object": true, "embed": true,
	"style": true, "link": true, "meta": true, "base": true,
}

// urlBearingAttrs lists attributes that can carry a URL we want to neutralise.
var urlBearingAttrs = map[string]bool{
	"href":       true,
	"src":        true,
	"action":     true,
	"formaction": true,
}

// sanitizeHTML parses s as an HTML fragment, removes dangerous elements,
// strips on* event handlers, and neutralises javascript:/vbscript: URLs.
func sanitizeHTML(s string) string {
	if s == "" {
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(s))
	if err != nil {
		return template.HTMLEscapeString(s)
	}
	doc.Find("*").Each(func(_ int, sel *goquery.Selection) {
		for _, n := range sel.Nodes {
			if dangerousTags[strings.ToLower(n.Data)] {
				sel.Remove()
				return
			}
			kept := n.Attr[:0]
			for _, a := range n.Attr {
				key := strings.ToLower(a.Key)
				if strings.HasPrefix(key, "on") {
					continue
				}
				if urlBearingAttrs[key] && unsafeURL(a.Val) {
					continue
				}
				kept = append(kept, a)
			}
			n.Attr = kept
		}
	})
	// goquery wraps fragments in <html><body>; emit just the body's children.
	body := doc.Find("body")
	if body.Length() == 0 {
		html, _ := doc.Html()
		return html
	}
	html, _ := body.Html()
	return html
}

// unsafeURL returns true for javascript:/vbscript: URLs and for data: URLs
// that aren't whitelisted raster images. SVG is excluded because data:image/
// svg+xml can carry inline <script>.
func unsafeURL(v string) bool {
	t := strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(t, "javascript:") || strings.HasPrefix(t, "vbscript:") {
		return true
	}
	if !strings.HasPrefix(t, "data:") {
		return false
	}
	return !strings.HasPrefix(t, "data:image/png") &&
		!strings.HasPrefix(t, "data:image/jpeg") &&
		!strings.HasPrefix(t, "data:image/gif") &&
		!strings.HasPrefix(t, "data:image/webp")
}
