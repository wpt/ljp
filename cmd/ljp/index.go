package main

import (
	"fmt"
	"html"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// archiveIndexTmpl renders {dir}/index.html — a table of contents for a
// --render --dir archive. Styling mirrors pkg/lj/post.html so the archive
// reads as one document set.
var archiveIndexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Archive ({{len .}} posts)</title>
<style>
  body { max-width: 720px; margin: 2em auto; padding: 0 1em; font-family: Georgia, serif; line-height: 1.6; color: #222; }
  h1 { margin-bottom: 0.2em; }
  .count { color: #666; font-size: 0.9em; margin-bottom: 1.5em; }
  ul { list-style: none; padding: 0; }
  li { padding: 0.25em 0; border-bottom: 1px solid #eee; }
  a { color: #1a3c6e; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .id { color: #888; font-size: 0.85em; margin-left: 0.5em; }
</style>
</head>
<body>
<h1>Archive</h1>
<div class="count">{{len .}} posts, newest first</div>
<ul>
{{range .}}<li><a href="{{.File}}">{{.Title}}</a><span class="id">#{{.ID}}</span></li>
{{end}}</ul>
</body>
</html>
`))

type indexEntry struct {
	ID    int
	File  string // actual filename — the href; may differ from ID.html for hand-placed files like 007.html
	Title string
}

// maybeWriteArchiveIndex regenerates {dir}/index.html after a render-to-dir
// run. Best-effort: the posts are already safely on disk, so an index failure
// is a warning, not a fatal error. A dir that was never created (the run
// matched zero posts) is a silent no-op, not a warning.
func maybeWriteArchiveIndex(render bool, dir string) {
	if !render || dir == "" {
		return
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return
	}
	if err := writeArchiveIndex(dir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: writing index.html: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(dir, "index.html"))
}

// writeArchiveIndex writes {dir}/index.html linking every {id}.html post page
// in dir, newest (highest ID) first. It lists the whole directory, not just
// the current run, so --resume runs keep prior posts in the index. Written
// atomically (tmp + rename) like the post files.
func writeArchiveIndex(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var posts []indexEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".html" {
			continue
		}
		// Non-numeric names (index.html itself, strays) are skipped.
		id, err := strconv.Atoi(strings.TrimSuffix(name, ".html"))
		if err != nil || id <= 0 {
			continue
		}
		if info, err := e.Info(); err != nil || info.Size() == 0 {
			continue // 0-byte leftover from a crashed run
		}
		title := readPostTitle(filepath.Join(dir, name))
		if title == "" {
			title = fmt.Sprintf("#%d", id)
		}
		posts = append(posts, indexEntry{ID: id, File: name, Title: title})
	}
	slices.SortFunc(posts, func(a, b indexEntry) int { return b.ID - a.ID })

	return writeFileAtomic(filepath.Join(dir, "index.html"), func(w io.Writer) error {
		return archiveIndexTmpl.Execute(w, posts)
	})
}

// titleRe matches the <title> element our own render template writes into
// every post page (see pkg/lj/post.html); it sits within the first few hundred
// bytes of the file.
var titleRe = regexp.MustCompile(`(?is)<title>(.*?)</title>`)

// readPostTitle extracts the <title> text from the head of a rendered post
// file. Returns "" if the file can't be read or carries no title — the caller
// falls back to the post ID. The stored title is HTML-escaped (html/template
// wrote it), so it's unescaped here; the index template re-escapes on output.
func readPostTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return ""
	}
	m := titleRe.FindSubmatch(head)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}
