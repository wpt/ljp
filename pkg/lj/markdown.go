package lj

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// HTMLToMarkdown converts HTML to Markdown. Handles the subset of HTML found in
// LiveJournal posts: p, div, br, b/strong, i/em, s/strike/del, a, img,
// blockquote, ul/ol/li (including nesting), h1-h6, pre/code, hr, tables, center.
func HTMLToMarkdown(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + htmlStr + "</div>"))
	if err != nil {
		return stripTags(htmlStr)
	}
	out := strings.TrimSpace(tidyMarkdown(nodeToMarkdown(doc.Find("div").First())))
	// A stray closing tag in the input can break the wrapper-<div> trick, leaving
	// real content as a sibling outside the div. If the wrapped parse came back
	// empty despite non-empty input, reparse without the wrapper.
	if out == "" && strings.TrimSpace(htmlStr) != "" {
		if doc2, err2 := goquery.NewDocumentFromReader(strings.NewReader(htmlStr)); err2 == nil {
			out = strings.TrimSpace(tidyMarkdown(nodeToMarkdown(doc2.Find("body"))))
		}
	}
	return out
}

func nodeToMarkdown(s *goquery.Selection) string {
	var buf strings.Builder
	s.Contents().Each(func(_ int, child *goquery.Selection) {
		buf.WriteString(renderNode(child))
	})
	return buf.String()
}

// renderNode renders a single DOM node to Markdown.
func renderNode(child *goquery.Selection) string {
	node := child.Get(0)
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return escapeMarkdown(node.Data)
	}
	if node.Type != html.ElementNode {
		return ""
	}

	switch goquery.NodeName(child) {
	case "br":
		return "\n"
	case "p":
		return "\n\n" + strings.TrimSpace(nodeToMarkdown(child)) + "\n\n"
	case "div":
		// Line-level container: one newline so div-per-line posts don't collapse
		// onto a single line, but not a full paragraph break.
		return "\n" + strings.TrimSpace(nodeToMarkdown(child)) + "\n"
	case "b", "strong":
		return emphasis("**", nodeToMarkdown(child))
	case "i", "em":
		return emphasis("*", nodeToMarkdown(child))
	case "s", "strike", "del":
		return emphasis("~~", nodeToMarkdown(child))
	case "code":
		// Inline code: take raw text so its contents aren't Markdown-escaped.
		return "`" + child.Text() + "`"
	case "pre":
		return "\n\n```\n" + strings.Trim(child.Text(), "\n") + "\n```\n\n"
	case "a":
		href, _ := child.Attr("href")
		text := strings.TrimSpace(nodeToMarkdown(child))
		if href != "" && text != "" {
			return fmt.Sprintf("[%s](%s)", text, href)
		}
		return text
	case "img":
		src, _ := child.Attr("src")
		alt, _ := child.Attr("alt")
		return fmt.Sprintf("![%s](%s)", alt, src)
	case "blockquote":
		inner := strings.TrimSpace(nodeToMarkdown(child))
		var buf strings.Builder
		buf.WriteString("\n\n")
		for _, line := range strings.Split(inner, "\n") {
			buf.WriteString("> ")
			buf.WriteString(line)
			buf.WriteString("\n")
		}
		buf.WriteString("\n")
		return buf.String()
	case "ul":
		return "\n" + renderList(child, false, 0) + "\n"
	case "ol":
		return "\n" + renderList(child, true, 0) + "\n"
	case "hr":
		return "\n\n---\n\n"
	case "h1":
		return "\n\n# " + strings.TrimSpace(nodeToMarkdown(child)) + "\n\n"
	case "h2":
		return "\n\n## " + strings.TrimSpace(nodeToMarkdown(child)) + "\n\n"
	case "h3":
		return "\n\n### " + strings.TrimSpace(nodeToMarkdown(child)) + "\n\n"
	case "h4", "h5", "h6":
		return "\n\n#### " + strings.TrimSpace(nodeToMarkdown(child)) + "\n\n"
	case "tr":
		return "\n" + strings.TrimSpace(nodeToMarkdown(child)) + "\n"
	case "td", "th":
		return strings.TrimSpace(nodeToMarkdown(child)) + " "
	case "center":
		return nodeToMarkdown(child)
	case "li":
		// li is normally consumed by renderList; if it appears loose, just emit
		// its content.
		return nodeToMarkdown(child)
	default:
		// Unknown tags: recurse into children.
		return nodeToMarkdown(child)
	}
}

// renderList renders a ul/ol at the given nesting depth. Nested lists inside an
// <li> are indented two spaces per level so the structure survives.
func renderList(list *goquery.Selection, ordered bool, depth int) string {
	var buf strings.Builder
	n := 0
	list.Children().Each(func(_ int, li *goquery.Selection) {
		if goquery.NodeName(li) != "li" {
			return
		}
		n++
		buf.WriteString(renderListItem(li, ordered, depth, n))
	})
	return buf.String()
}

func renderListItem(li *goquery.Selection, ordered bool, depth, n int) string {
	indent := strings.Repeat("  ", depth)
	marker := "- "
	if ordered {
		marker = fmt.Sprintf("%d. ", n)
	}
	var inline, nested strings.Builder
	li.Contents().Each(func(_ int, child *goquery.Selection) {
		switch goquery.NodeName(child) {
		case "ul":
			nested.WriteString(renderList(child, false, depth+1))
		case "ol":
			nested.WriteString(renderList(child, true, depth+1))
		default:
			inline.WriteString(renderNode(child))
		}
	})
	return indent + marker + strings.TrimSpace(inline.String()) + "\n" + nested.String()
}

// emphasis wraps inner in marker, but moves any leading/trailing whitespace
// outside the markers — "** text **" is invalid CommonMark emphasis, "**text**"
// with the spaces around it is correct.
func emphasis(marker, inner string) string {
	trimmedLeft := strings.TrimLeft(inner, " \t\n")
	lead := inner[:len(inner)-len(trimmedLeft)]
	core := strings.TrimRight(trimmedLeft, " \t\n")
	trail := trimmedLeft[len(core):]
	if core == "" {
		return inner // nothing to emphasize
	}
	return lead + marker + core + marker + trail
}

// mdEscaper escapes the Markdown metacharacters that would otherwise turn
// literal post text (a*b, file_name, [x]) into formatting. Backslash is escaped
// first via the replacer's left-to-right single pass.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
)

func escapeMarkdown(s string) string {
	return mdEscaper.Replace(s)
}

// tidyMarkdown collapses runs of blank lines to a single blank line and trims
// trailing whitespace, so the per-node "\n\n" block padding doesn't pile up.
func tidyMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// htmlToText converts HTML to plain text, inserting newlines at block
// boundaries so words don't glue across paragraphs/lines (goquery's .Text()
// concatenates with no separators).
func htmlToText(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + htmlStr + "</div>"))
	if err != nil {
		return tidyText(stripTags(htmlStr))
	}
	var buf strings.Builder
	textWalk(doc.Find("div").First(), &buf)
	out := tidyText(buf.String())
	if out == "" && strings.TrimSpace(htmlStr) != "" {
		// Stray-tag fallback, as in HTMLToMarkdown.
		if doc2, err2 := goquery.NewDocumentFromReader(strings.NewReader(htmlStr)); err2 == nil {
			var b2 strings.Builder
			textWalk(doc2.Find("body"), &b2)
			out = tidyText(b2.String())
		}
	}
	return out
}

func textWalk(s *goquery.Selection, buf *strings.Builder) {
	s.Contents().Each(func(_ int, child *goquery.Selection) {
		node := child.Get(0)
		if node == nil {
			return
		}
		if node.Type == html.TextNode {
			buf.WriteString(node.Data)
			return
		}
		if node.Type != html.ElementNode {
			return
		}
		switch goquery.NodeName(child) {
		case "br":
			buf.WriteString("\n")
		case "p", "div", "li", "blockquote", "tr", "ul", "ol", "table",
			"h1", "h2", "h3", "h4", "h5", "h6", "pre", "hr":
			buf.WriteString("\n")
			textWalk(child, buf)
			buf.WriteString("\n")
		case "td", "th":
			textWalk(child, buf)
			buf.WriteString(" ")
		default:
			textWalk(child, buf)
		}
	})
}

// tidyText trims each line and collapses runs of blank lines to one.
func tidyText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			blanks++
			if blanks > 1 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripTags(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr
	}
	return doc.Text()
}
