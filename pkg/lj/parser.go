package lj

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"
)

const maxCommentPages = 1000

var (
	// postLinkRe matches a post-id .html segment, anchored on .html$ (with
	// optional ? query) so /tag/foo.html and bare /N stop matching. Cross-journal
	// and /tag/ hrefs are filtered inline in ParseJournalIndex before this is applied.
	postLinkRe     = regexp.MustCompile(`/(\d+)\.html(?:\?|$)`)
	sitePageMarker = []byte("Site.page = ")
)

// ParsePost fetches one LiveJournal post by (user, id) and returns the parsed
// [Post]. Populates Title, Date, Body, Tags, ReplyCount, and OG. Comments are
// NOT populated — call ParseComments separately and stitch the result into
// post.Comments. Body format follows client.BodyFormat (default HTML); when
// client.ImagesDir is non-empty, inline <img> URLs are downloaded into that
// directory and src attributes rewritten to local paths. Image download is
// skipped for FormatText (the rewritten src would be discarded by the
// plain-text conversion anyway).
func ParsePost(ctx context.Context, client *Client, user string, id int) (*Post, error) {
	resp, err := client.Get(ctx, client.postURL(user, id))
	if err != nil {
		return nil, fmt.Errorf("fetch post: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read post: %w", err)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	post := &Post{
		ID:     id,
		URL:    client.postURL(user, id),
		Author: user,
	}

	if sp, err := extractSitePage(body); err == nil {
		post.ReplyCount = sp.ReplyCount
	} else {
		client.log().Warn("extract Site.page failed", "user", user, "id", id, "err", err)
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
		format := client.BodyFormat
		switch format {
		case "":
			format = FormatHTML
		case FormatHTML, FormatMarkdown, FormatText:
			// ok
		default:
			client.log().Warn("unknown body format, using html", "format", format)
			format = FormatHTML
		}

		// Skip image download for text output — the rewritten <img src> would be
		// thrown away by bodyEl.Text() anyway, so don't waste the bandwidth.
		var bodyHTML string
		if format != FormatText {
			bodyHTML, _ = bodyEl.Html()
			if client.ImagesDir != "" {
				bodyHTML = downloadImages(ctx, client, bodyHTML)
			}
		}

		switch format {
		case FormatText:
			// Convert the raw body HTML (no image download needed for text).
			raw, _ := bodyEl.Html()
			post.Body = htmlToText(raw)
		case FormatMarkdown:
			post.Body = HTMLToMarkdown(bodyHTML)
		default: // FormatHTML
			post.Body = bodyHTML
		}
	} else {
		// No body container on a 200 page (adult-content interstitial for logged-
		// out visitors, suspended-journal notice, or a markup change). Warn so a
		// --resume user can tell which IDs came back gutted and re-fetch them.
		client.log().Warn("post body container not found", "user", user, "id", id)
	}

	// Scope tag extraction to the dedicated tags block. The whole .aentry-post
	// article also contains the body, so a /tag/ link written inside the post
	// text ("see my posts tagged X") must NOT leak into Post.Tags. .aentry-tags
	// is current LJ markup; .aentry-post__tags covers older/styled pages.
	var tags []string
	doc.Find(".aentry-tags a[href*='/tag/'], .aentry-post__tags a[href*='/tag/']").Each(func(_ int, s *goquery.Selection) {
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

// ParseComments fetches every page of the post's flat-view comments in
// parallel (bounded by client.HTTPConcurrency) and returns a tree built via
// BuildCommentTree. The flat view supplies the ParentID for each comment
// directly, so a single set of fetches is enough — no separate threaded-view
// pass is needed.
func ParseComments(ctx context.Context, client *Client, user string, id int) ([]*Comment, error) {
	// Fetch flat view: it already supplies `parent` per comment, so the threaded
	// view fetch (previously done for the parent map) is redundant.
	firstComments, maxPage, err := fetchCommentsPage(ctx, client, user, id, 1, true)
	if err != nil {
		return nil, err
	}

	all := firstComments
	if maxPage > 1 {
		// Pages 2..maxPage in parallel — bound by client.HTTPConcurrency (which also
		// caps the underlying http.Transport's MaxConnsPerHost so we don't open more
		// sockets than we can use).
		eg, ectx := errgroup.WithContext(ctx)
		eg.SetLimit(client.concurrency())
		results := make([][]*Comment, maxPage-1)
		for page := 2; page <= maxPage; page++ {
			idx := page - 2
			eg.Go(func() error {
				// Only page 1 needs pagination detection; skip the full DOM parse
				// detectMaxPage does on every later page.
				comments, _, err := fetchCommentsPage(ectx, client, user, id, page, false)
				if err != nil {
					return fmt.Errorf("flat page %d: %w", page, err)
				}
				results[idx] = comments
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return nil, err
		}
		for _, page := range results {
			all = append(all, page...)
		}
	}

	return BuildCommentTree(all), nil
}

// formatBody converts raw body HTML to the client's configured BodyFormat.
// Shared by the post body and comment bodies so output is consistent across
// both. Unknown/empty formats fall through to raw HTML.
func formatBody(client *Client, raw string) string {
	switch client.BodyFormat {
	case FormatMarkdown:
		return HTMLToMarkdown(raw)
	case FormatText:
		return htmlToText(raw)
	default:
		return raw
	}
}

// fetchCommentsPage fetches one flat-view comment page. detectPages controls
// whether the page is parsed for the pagination max — only needed for page 1,
// so callers fetching pages 2..N pass false to skip a full DOM parse.
func fetchCommentsPage(ctx context.Context, client *Client, user string, id, page int, detectPages bool) ([]*Comment, int, error) {
	log := client.log()
	url := client.commentsURL(user, id, page)
	log.Debug("fetching comments page", "page", page)
	resp, err := client.Get(ctx, url)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch comments page %d: %w", page, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read comments page %d: %w", page, err)
	}

	maxPage := 1
	if detectPages {
		maxPage = detectMaxPage(body, log)
	}

	sp, err := extractSitePage(body)
	if err != nil {
		return nil, 0, fmt.Errorf("extract Site.page page=%d: %w", page, err)
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
			// Honour BodyFormat so `--format markdown/text` comments match the
			// post body instead of staying raw HTML.
			Body:     formatBody(client, rc.Article),
			Subject:  rc.Subject,
			Userpic:  rc.Userpic,
			Deleted:  rc.Deleted != 0,
		}
		comments = append(comments, c)
	}
	log.Debug("comments page fetched", "page", page, "count", len(comments))

	return comments, maxPage, nil
}

// detectMaxPage finds the highest pagination page number by scanning <a> hrefs
// that look like pagination links (have BOTH page=N and format=light query params).
// Returns 1 if no pagination links are found. Caps at maxCommentPages.
func detectMaxPage(body []byte, log *slog.Logger) int {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return 1
	}
	maxN := 1
	doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, _ := s.Attr("href")
		u, err := url.Parse(href)
		if err != nil {
			return true
		}
		q := u.Query()
		if q.Get("format") != "light" {
			return true
		}
		n, err := strconv.Atoi(q.Get("page"))
		if err != nil || n <= maxN {
			return true
		}
		maxN = n
		return true
	})
	if maxN > maxCommentPages {
		log.Warn("comment pagination exceeds cap, truncating", "detected", maxN, "cap", maxCommentPages)
		maxN = maxCommentPages
	}
	return maxN
}

// extractSitePage finds the real Site.page object. The literal "Site.page = "
// can appear earlier in the document inside the rendered post body (e.g. a post
// quoting LJ's own JS), so we can't just take the first occurrence. We brace-
// match every occurrence, JSON-decode the candidates, and keep the one with the
// most comments — the genuine comments script — breaking ties toward the later
// occurrence (it follows the body).
func extractSitePage(html []byte) (*sitePage, error) {
	var best *sitePage
	var lastErr error
	for off := 0; ; {
		jsonBytes, next, err := extractJSONFrom(html, off)
		if err != nil {
			break // no more markers
		}
		off = next
		var sp sitePage
		if uerr := json.Unmarshal(jsonBytes, &sp); uerr != nil {
			lastErr = fmt.Errorf("parse Site.page JSON: %w", uerr)
			continue
		}
		if best == nil || len(sp.Comments) >= len(best.Comments) {
			spCopy := sp
			best = &spCopy
		}
	}
	if best != nil {
		return best, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("site.page marker not found")
}

// extractJSON returns the first Site.page object (brace-matched). Retained for
// the brace-matcher unit tests; extractSitePage uses extractJSONFrom to scan
// all occurrences.
func extractJSON(html []byte) ([]byte, error) {
	js, _, err := extractJSONFrom(html, 0)
	return js, err
}

// extractJSONFrom brace-matches the Site.page object at the first marker at or
// after `from`, returning the JSON bytes and the offset just past that marker
// (so the caller can resume scanning for later occurrences).
func extractJSONFrom(html []byte, from int) (js []byte, next int, err error) {
	rel := bytes.Index(html[from:], sitePageMarker)
	if rel == -1 {
		return nil, 0, fmt.Errorf("site.page marker not found")
	}
	idx := from + rel
	next = idx + len(sitePageMarker)
	start := bytes.IndexByte(html[idx:], '{')
	if start == -1 {
		return nil, next, fmt.Errorf("no opening brace after Site.page")
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
				return html[start : i+1], next, nil
			}
		}
	}
	return nil, next, fmt.Errorf("unmatched braces in Site.page")
}

// imageExtAllowlist is the set of file extensions we accept verbatim from the
// URL path. Anything outside this set is treated as missing-extension (guessed)
// so the Content-Type fallback can correct it. Caps weird URLs like
// /foo.<1MB-of-junk> from producing pathological filenames.
var imageExtAllowlist = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".avif": true,
	".bmp":  true,
}

// downloadImages finds all <img src> in HTML, downloads images concurrently
// into client.ImagesDir, and rewrites src to the local filesystem path so the
// resulting HTML resolves to the downloaded copy when opened in a browser.
// Skips data:/javascript:/vbscript: URIs and non-http(s) URLs so they don't
// burn retries against an unsupported scheme.
func downloadImages(ctx context.Context, client *Client, html string) string {
	log := client.log()
	if err := os.MkdirAll(client.ImagesDir, 0755); err != nil {
		log.Warn("mkdir failed", "dir", client.ImagesDir, "err", err)
		return html
	}

	root, err := os.OpenRoot(client.ImagesDir)
	if err != nil {
		log.Warn("open root failed", "dir", client.ImagesDir, "err", err)
		return html
	}
	defer root.Close()

	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<div>" + html + "</div>"))
	if err != nil {
		return html
	}

	type job struct {
		sels       []*goquery.Selection // all <img> with this src — share one download
		fetchURL   string               // URL to GET (// normalized to https:)
		filename   string
		extGuessed bool // true when URL had no extension; allow Content-Type override
		resolved   bool // already on disk from a prior run — skip download, just rewrite
	}
	var jobs []job
	bySrc := map[string]int{} // cleaned src → jobs index (dedup so duplicate <img> don't race)
	doc.Find("img[src]").Each(func(_ int, s *goquery.Selection) {
		// Browsers tolerate leading/trailing whitespace in src; hand-written old
		// posts contain it. Trim before parsing or url.Parse rejects it.
		rawSrc, _ := s.Attr("src")
		src := strings.TrimSpace(rawSrc)
		if src == "" {
			return
		}
		// Dedup: multiple <img src=X> in the same post download once.
		if idx, ok := bySrc[src]; ok {
			jobs[idx].sels = append(jobs[idx].sels, s)
			return
		}
		// Hard-block embedded/dangerous schemes before any network work.
		lower := strings.ToLower(src)
		if strings.HasPrefix(lower, "data:") ||
			strings.HasPrefix(lower, "javascript:") ||
			strings.HasPrefix(lower, "vbscript:") {
			return
		}
		// Protocol-relative URLs (//host/pic.jpg) were a common embedding style
		// in 2010s posts and carry their host — fetch them over https rather than
		// silently dropping the image and leaving a file:// -breaking src.
		fetchURL := src
		if strings.HasPrefix(src, "//") {
			fetchURL = "https:" + src
		}
		u, err := url.Parse(fetchURL)
		if err != nil {
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return // skip bare-relative or unsupported-scheme URLs
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(src)))[:16]
		ext := strings.ToLower(path.Ext(u.Path))
		guessed := false
		// Apply an allowlist — anything weird falls through to the
		// Content-Type-based rename so we never end up with {hash}.html /
		// {hash}.<1MB-of-junk> / {hash}.exe on disk. Allowlist also caps
		// length implicitly (longest entry is ".jpeg").
		if !imageExtAllowlist[ext] {
			ext = ".jpg"
			guessed = true
		}
		filename := hash + ext
		resolved := false
		// A prior run may have saved this extension-less image under its real
		// extension (Content-Type rename below). The skip-check stats the exact
		// name, so probe known siblings first to avoid re-downloading it forever.
		if guessed {
			if existing := findExistingImage(root, hash); existing != "" {
				filename = existing
				resolved = true
			}
		}
		bySrc[src] = len(jobs)
		jobs = append(jobs, job{
			sels:       []*goquery.Selection{s},
			fetchURL:   fetchURL,
			filename:   filename,
			extGuessed: guessed && !resolved,
			resolved:   resolved,
		})
	})

	// Download concurrently — bounded by client.HTTPConcurrency, which matches
	// the Transport's MaxConnsPerHost so we don't queue more sockets than exist.
	// Each goroutine writes to its own index in ok[] and contentTypes[], so no
	// mutex is needed.
	eg, ectx := errgroup.WithContext(ctx)
	eg.SetLimit(client.concurrency())
	ok := make([]bool, len(jobs))
	contentTypes := make([]string, len(jobs))
	for i, j := range jobs {
		if j.resolved {
			ok[i] = true // already on disk from a prior run; just rewrite the src
			continue
		}
		eg.Go(func() error {
			ct, err := client.downloadInto(ectx, root, j.fetchURL, j.filename)
			if err != nil {
				log.Warn("image download failed", "url", j.fetchURL, "err", err)
				return nil
			}
			ok[i] = true
			contentTypes[i] = ct
			return nil
		})
	}
	_ = eg.Wait()

	// Rewrite src after all downloads settle (goquery mutation isn't goroutine-safe).
	okCount := 0
	for i := range jobs {
		if !ok[i] {
			continue
		}
		okCount++
		// If the URL had no extension and the server told us the real type,
		// rename to the correct extension so PNG/GIF/WebP downloads from
		// extension-less URLs aren't mis-saved as .jpg.
		if jobs[i].extGuessed {
			if newExt := extFromContentType(contentTypes[i]); newExt != "" && newExt != ".jpg" {
				base := strings.TrimSuffix(jobs[i].filename, ".jpg")
				newName := base + newExt
				if err := root.Rename(jobs[i].filename, newName); err == nil {
					jobs[i].filename = newName
				}
			}
		}
		newSrc := filepath.ToSlash(filepath.Join(client.ImagesDir, jobs[i].filename))
		for _, sel := range jobs[i].sels {
			sel.SetAttr("src", newSrc)
		}
	}
	if len(jobs) > 0 {
		log.Debug("image download summary", "total", len(jobs), "ok", okCount, "failed", len(jobs)-okCount)
	}

	result, _ := doc.Find("div").First().Html()
	return result
}

// findExistingImage returns the name of an already-downloaded image for hash,
// if any sibling {hash}.<ext> exists with non-zero size. Lets a re-run reuse a
// file a prior run renamed from the guessed .jpg to its real extension instead
// of re-downloading it (the skip-check only stats one exact name).
func findExistingImage(root *os.Root, hash string) string {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif", ".bmp"} {
		name := hash + ext
		if st, err := root.Stat(name); err == nil && st.Size() > 0 {
			return name
		}
	}
	return ""
}

// extFromContentType maps a Content-Type header to a file extension.
// Returns "" when the type isn't a known image. SVG is intentionally
// excluded because RenderPost's sanitizer rejects data:image/svg URLs;
// keeping .svg files would invite confusion.
func extFromContentType(ct string) string {
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(strings.ToLower(ct)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	case "image/bmp", "image/x-ms-bmp":
		return ".bmp"
	}
	return ""
}

// ParsePostURL extracts (user, id) from a LiveJournal post URL. Accepts
// http:// or https:// schemes, requires a <user>.livejournal.com host (no
// lookalikes), and a numeric post ID with an optional ".html" suffix. Returns
// an error on malformed input, non-LJ hosts, or non-numeric post IDs.
func ParsePostURL(rawURL string) (user string, id int, err error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimSuffix(rawURL, "/")

	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", 0, fmt.Errorf("invalid LJ URL: %s", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", 0, fmt.Errorf("unsupported URL scheme %q (want http/https)", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	const suffix = ".livejournal.com"

	// users.livejournal.com serves underscore-username journals (which can't be
	// subdomains) at the path form /<user>/<id>.html. Parse user+id from path.
	if host == "users.livejournal.com" {
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) < 2 || segs[0] == "" {
			return "", 0, fmt.Errorf("invalid users.livejournal.com URL: %s", rawURL)
		}
		id, err = parsePostID(segs[len(segs)-1])
		if err != nil {
			return "", 0, err
		}
		return segs[0], id, nil
	}

	if !strings.HasSuffix(host, suffix) {
		return "", 0, fmt.Errorf("not a livejournal URL: %s", rawURL)
	}
	user = strings.TrimSuffix(host, suffix)
	if user == "" || strings.Contains(user, ".") {
		return "", 0, fmt.Errorf("not a livejournal URL: %s", rawURL)
	}
	// Reserved subdomains aren't journals — don't silently treat them as one.
	switch user {
	case "www", "users", "m", "l", "community":
		return "", 0, fmt.Errorf("not a journal subdomain: %s", host)
	}

	id, err = parsePostID(path.Base(u.Path))
	if err != nil {
		return "", 0, err
	}
	return user, id, nil
}

// parsePostID parses a trailing post-ID segment (optionally with .html) and
// rejects non-numeric or non-positive IDs.
func parsePostID(seg string) (int, error) {
	seg = strings.TrimSuffix(seg, ".html")
	id, err := strconv.Atoi(seg)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid post ID in URL: %s", seg)
	}
	return id, nil
}

// ParseJournalIndex extracts post IDs from a journal index page HTML. It keeps
// only this journal's post links: relative hrefs and absolute hrefs whose host
// equals journalHost (the form real LiveJournal actually emits —
// https://<user>.livejournal.com/<id>.html). Cross-journal sidebar links,
// /tag/ pages, and "recent comments" (?thread=) links are rejected; the latter
// point at posts from arbitrary dates and would corrupt chronological ordering.
// Pass the journal's host (see Client.journalHost); an empty journalHost falls
// back to relative-only matching.
func ParseJournalIndex(body io.Reader, journalHost string) ([]int, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("parse journal index: %w", err)
	}
	seen := make(map[int]bool)
	var ids []int
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		// Same-journal only: relative (no host) or absolute pointing at this
		// journal's host. Foreign hosts are sidebar/cross-journal links.
		if u.Host != "" && !strings.EqualFold(u.Host, journalHost) {
			return
		}
		// Reject /tag/ pages and ?thread= "recent comments" links.
		if strings.Contains(u.Path, "/tag/") || u.Query().Has("thread") {
			return
		}
		if m := postLinkRe.FindStringSubmatch(u.Path); m != nil {
			id, _ := strconv.Atoi(m[1])
			if id > 0 && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	})
	return ids, nil
}
