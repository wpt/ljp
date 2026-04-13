package lj

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	pageRe         = regexp.MustCompile(`[?&]page=(\d+)`)
	postLinkRe     = regexp.MustCompile(`/(\d+)\.html`)
	sitePageMarker = []byte("Site.page")
)

func ParsePost(ctx context.Context, client *Client, user string, id int) (*Post, error) {
	resp, err := client.Get(ctx, client.postURL(user, id))
	if err != nil {
		return nil, fmt.Errorf("fetch post: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	post := &Post{
		ID:     id,
		URL:    client.postURL(user, id),
		Author: user,
	}

	post.Title = strings.TrimSpace(doc.Find("h1.aentry-post__title").Text())
	if post.Title == "" {
		post.Title = strings.TrimSpace(doc.Find("h1").First().Text())
	}

	timeEl := doc.Find("time").First()
	post.Date = strings.TrimSpace(timeEl.Text())
	if t := ParseDate(post.Date); !t.IsZero() {
		post.DateUnix = t.Unix()
	}

	bodyEl := doc.Find("div.aentry-post__text")
	if bodyEl.Length() > 0 {
		bodyHTML, _ := bodyEl.Html()

		// Download images if configured
		if client.ImagesDir != "" {
			bodyHTML = downloadImages(ctx, client, bodyHTML)
		}

		// Convert body to requested format
		format := client.BodyFormat
		if format == "" {
			format = "html"
		}
		switch format {
		case "html":
			post.Body = bodyHTML
		case "text":
			post.Body = strings.TrimSpace(bodyEl.Text())
		case "markdown":
			post.Body = HTMLToMarkdown(bodyHTML)
		default:
			post.Body = bodyHTML
		}
	}

	var tags []string
	doc.Find("a[href*='/tag/']").Each(func(_ int, s *goquery.Selection) {
		if tag := strings.TrimSpace(s.Text()); tag != "" {
			tags = append(tags, tag)
		}
	})
	post.Tags = tags

	og := &OGMeta{}
	og.Title, _ = doc.Find(`meta[property="og:title"]`).Attr("content")
	og.Description, _ = doc.Find(`meta[property="og:description"]`).Attr("content")
	og.Image, _ = doc.Find(`meta[property="og:image"]`).Attr("content")
	if og.Title != "" || og.Description != "" || og.Image != "" {
		post.OG = og
	}

	return post, nil
}

func ParseComments(ctx context.Context, client *Client, user string, id int) ([]*Comment, error) {
	// 1. Fetch flat view (all bodies, no parent info)
	firstComments, maxPage, err := fetchCommentsPage(ctx, client, user, id, 1)
	if err != nil {
		return nil, err
	}

	all := firstComments
	for page := 2; page <= maxPage; page++ {
		comments, _, err := fetchCommentsPage(ctx, client, user, id, page)
		if err != nil {
			return nil, fmt.Errorf("flat page %d: %w", page, err)
		}
		all = append(all, comments...)
	}

	// 2. Fetch threaded view (parent info, may have loaded:0)
	parentMap := make(map[int]int) // dtalkid -> parent dtalkid
	firstThreaded, maxPageT, err := fetchThreadedPage(ctx, client, user, id, 1)
	if err != nil {
		client.log("Warning: threaded view for %d: %v (comments will be flat)\n", id, err)
	} else {
		for _, c := range firstThreaded {
			parentMap[c.DTalkID] = c.Parent
		}
		for page := 2; page <= maxPageT; page++ {
			threaded, _, err := fetchThreadedPage(ctx, client, user, id, page)
			if err != nil {
				client.log("Warning: threaded page %d for %d: %v\n", page, id, err)
				break
			}
			for _, c := range threaded {
				parentMap[c.DTalkID] = c.Parent
			}
		}
	}

	// 3. Merge: apply parent info from threaded to flat comments
	if len(parentMap) > 0 {
		for _, c := range all {
			if p, ok := parentMap[c.ID]; ok {
				c.ParentID = p
			}
		}
	}

	return BuildCommentTree(all), nil
}

func fetchThreadedPage(ctx context.Context, client *Client, user string, id, page int) ([]rawComment, int, error) {
	url := client.commentsThreadedURL(user, id, page)
	client.log("  threaded page %d...\n", page)
	resp, err := client.Get(ctx, url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	maxPage := 1
	for _, m := range pageRe.FindAllSubmatch(body, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > maxPage {
			maxPage = n
		}
	}

	sp, err := extractSitePage(body)
	if err != nil {
		return nil, 0, err
	}

	client.log("  threaded page %d: %d comments\n", page, len(sp.Comments))
	return sp.Comments, maxPage, nil
}

func fetchCommentsPage(ctx context.Context, client *Client, user string, id, page int) ([]*Comment, int, error) {
	url := client.commentsURL(user, id, page)
	client.log("  comments page %d...\n", page)
	resp, err := client.Get(ctx, url)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch comments page %d: %w", page, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	maxPage := 1
	for _, m := range pageRe.FindAllSubmatch(body, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > maxPage {
			maxPage = n
		}
	}

	sp, err := extractSitePage(body)
	if err != nil {
		return nil, 0, err
	}

	var comments []*Comment
	for _, rc := range sp.Comments {
		c := &Comment{
			ID:       rc.DTalkID,
			TalkID:   rc.TalkID,
			ParentID: rc.Parent,
			Level:    rc.Level,
			Author:   rc.DName,
			Username: rc.UName,
			Date:     rc.CTime,
			DateUnix: rc.CTimeTS,
			Body:     rc.Article,
			Subject:  rc.Subject,
			Userpic:  rc.Userpic,
			Deleted:  rc.Deleted != 0,
		}
		comments = append(comments, c)
	}
	client.log("  comments page %d: %d comments\n", page, len(comments))

	return comments, maxPage, nil
}

func extractSitePage(html []byte) (*sitePage, error) {
	jsonBytes, err := extractJSON(html)
	if err != nil {
		return nil, err
	}
	var sp sitePage
	if err := json.Unmarshal(jsonBytes, &sp); err != nil {
		return nil, fmt.Errorf("parse Site.page JSON: %w", err)
	}
	return &sp, nil
}

func extractJSON(html []byte) ([]byte, error) {
	idx := bytes.Index(html, sitePageMarker)
	if idx == -1 {
		return nil, fmt.Errorf("Site.page not found")
	}
	start := bytes.IndexByte(html[idx:], '{')
	if start == -1 {
		return nil, fmt.Errorf("no opening brace after Site.page")
	}
	start += idx

	depth := 0
	inString := false
	escape := false
	for i := start; i < len(html); i++ {
		ch := html[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1], nil
			}
		}
	}
	return nil, fmt.Errorf("unmatched braces in Site.page")
}

// downloadImages finds all <img src="..."> in HTML, downloads images, replaces URLs.
func downloadImages(ctx context.Context, client *Client, html string) string {
	os.MkdirAll(client.ImagesDir, 0755)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + html + "</div>"))
	if err != nil {
		return html
	}

	doc.Find("img[src]").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if src == "" {
			return
		}

		// Hash URL for unique filename
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(src)))[:16]
		ext := path.Ext(strings.Split(src, "?")[0])
		if ext == "" {
			ext = ".jpg"
		}
		filename := hash + ext
		destPath := filepath.Join(client.ImagesDir, filename)

		if err := client.DownloadFile(ctx, src, destPath); err != nil {
			client.log("Warning: image %s: %v\n", src, err)
			return
		}

		s.SetAttr("src", filepath.Join(client.ImagesDir, filename))
	})

	result, _ := doc.Find("div").First().Html()
	return result
}

func ParsePostURL(rawURL string) (user string, id int, err error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimSuffix(rawURL, "/")

	parts := strings.Split(rawURL, "/")
	if len(parts) < 4 {
		return "", 0, fmt.Errorf("invalid LJ URL: %s", rawURL)
	}

	host := parts[2]
	hostParts := strings.Split(host, ".")
	if len(hostParts) < 3 || hostParts[1] != "livejournal" {
		return "", 0, fmt.Errorf("not a livejournal URL: %s", rawURL)
	}
	user = hostParts[0]

	last := parts[len(parts)-1]
	last = strings.Split(last, "?")[0]
	last = strings.TrimSuffix(last, ".html")
	id, err = strconv.Atoi(last)
	if err != nil {
		return "", 0, fmt.Errorf("invalid post ID in URL: %s", last)
	}

	return user, id, nil
}

// ParseJournalIndex extracts post IDs from a journal index page HTML.
func ParseJournalIndex(body io.Reader) ([]int, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	var ids []int
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if m := postLinkRe.FindStringSubmatch(href); m != nil {
			id, _ := strconv.Atoi(m[1])
			if id > 0 && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	})
	return ids, nil
}

