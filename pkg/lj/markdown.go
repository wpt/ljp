package lj

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// HTMLToMarkdown converts HTML to Markdown. Handles the subset of HTML
// found in LiveJournal posts: p, br, b/strong, i/em, a, img, blockquote, ul/ol/li, h1-h6.
func HTMLToMarkdown(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + html + "</div>"))
	if err != nil {
		return stripTags(html)
	}
	return strings.TrimSpace(nodeToMarkdown(doc.Find("div").First()))
}

func nodeToMarkdown(s *goquery.Selection) string {
	var buf strings.Builder
	s.Contents().Each(func(_ int, child *goquery.Selection) {
		node := child.Get(0)
		if node == nil {
			return
		}

		// Text node
		if node.Type == 1 { // html.TextNode
			buf.WriteString(node.Data)
			return
		}

		tag := goquery.NodeName(child)
		switch tag {
		case "br":
			buf.WriteString("\n")
		case "p":
			buf.WriteString("\n\n")
			buf.WriteString(nodeToMarkdown(child))
			buf.WriteString("\n\n")
		case "b", "strong":
			buf.WriteString("**")
			buf.WriteString(nodeToMarkdown(child))
			buf.WriteString("**")
		case "i", "em":
			buf.WriteString("*")
			buf.WriteString(nodeToMarkdown(child))
			buf.WriteString("*")
		case "a":
			href, _ := child.Attr("href")
			text := nodeToMarkdown(child)
			if href != "" && text != "" {
				buf.WriteString(fmt.Sprintf("[%s](%s)", text, href))
			} else {
				buf.WriteString(text)
			}
		case "img":
			src, _ := child.Attr("src")
			alt, _ := child.Attr("alt")
			buf.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
		case "blockquote":
			lines := strings.Split(strings.TrimSpace(nodeToMarkdown(child)), "\n")
			for _, line := range lines {
				buf.WriteString("> ")
				buf.WriteString(line)
				buf.WriteString("\n")
			}
		case "ul":
			buf.WriteString("\n")
			child.Children().Each(func(_ int, li *goquery.Selection) {
				if goquery.NodeName(li) == "li" {
					buf.WriteString("- ")
					buf.WriteString(strings.TrimSpace(nodeToMarkdown(li)))
					buf.WriteString("\n")
				}
			})
		case "ol":
			buf.WriteString("\n")
			n := 0
			child.Children().Each(func(_ int, li *goquery.Selection) {
				if goquery.NodeName(li) == "li" {
					n++
					buf.WriteString(fmt.Sprintf("%d. ", n))
					buf.WriteString(strings.TrimSpace(nodeToMarkdown(li)))
					buf.WriteString("\n")
				}
			})
		case "h1":
			buf.WriteString("\n# ")
			buf.WriteString(strings.TrimSpace(nodeToMarkdown(child)))
			buf.WriteString("\n\n")
		case "h2":
			buf.WriteString("\n## ")
			buf.WriteString(strings.TrimSpace(nodeToMarkdown(child)))
			buf.WriteString("\n\n")
		case "h3":
			buf.WriteString("\n### ")
			buf.WriteString(strings.TrimSpace(nodeToMarkdown(child)))
			buf.WriteString("\n\n")
		case "h4", "h5", "h6":
			buf.WriteString("\n#### ")
			buf.WriteString(strings.TrimSpace(nodeToMarkdown(child)))
			buf.WriteString("\n\n")
		case "center":
			buf.WriteString(nodeToMarkdown(child))
		case "li":
			buf.WriteString(nodeToMarkdown(child))
		default:
			// Unknown tags: just recurse into children
			buf.WriteString(nodeToMarkdown(child))
		}
	})
	return buf.String()
}

func stripTags(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}
	return doc.Text()
}
