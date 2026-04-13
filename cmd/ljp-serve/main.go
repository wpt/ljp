package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/wpt/ljp/pkg/lj"
)

//go:embed post.html
var postTemplateFile embed.FS

var postTmpl = template.Must(
	template.New("post.html").Funcs(template.FuncMap{
		"raw": func(s string) template.HTML { return template.HTML(s) },
	}).ParseFS(postTemplateFile, "post.html"),
)

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.User}}</title>
<style>
  body { max-width: 720px; margin: 2em auto; padding: 0 1em; font-family: Georgia, serif; line-height: 1.6; color: #222; }
  a { color: #1a0dab; }
  .meta { color: #666; font-size: 0.85em; }
  .post { margin-bottom: 1em; }
  h1 { margin-bottom: 0.5em; }
  .stats { color: #666; margin-bottom: 1.5em; }
</style>
</head><body>
<h1>{{.User}}</h1>
<div class="stats">{{len .Posts}} posts</div>
{{range .Posts}}
<div class="post">
  <a href="/post/{{.ID}}">{{if .Title}}{{.Title}}{{else}}#{{.ID}}{{end}}</a>
  <span class="meta">{{.Date}} {{if .CommentCount}}({{.CommentCount}} comments){{end}}</span>
</div>
{{end}}
</body></html>`))

type postEntry struct {
	ID           int
	Title        string
	Date         string
	CommentCount int
}

func main() {
	dir := flag.String("dir", ".", "directory with JSON post files")
	addr := flag.String("addr", ":80", "listen address")
	images := flag.String("images", "", "images directory to serve at /images/")
	flag.Parse()

	user := flag.Arg(0)
	if user == "" {
		user = "journal"
	}

	mux := http.NewServeMux()

	var imagesDir string
	if *images != "" {
		imagesDir, _ = filepath.Abs(*images)
		mux.Handle("/images/", http.StripPrefix("/images/", http.FileServer(http.Dir(*images))))
	}

	mux.HandleFunc("/post/", func(w http.ResponseWriter, r *http.Request) {
		idStr := strings.TrimPrefix(r.URL.Path, "/post/")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(w, "invalid post ID", 400)
			return
		}

		path := filepath.Join(*dir, fmt.Sprintf("%d.json", id))
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "post not found", 404)
			return
		}

		var post lj.Post
		if err := json.Unmarshal(data, &post); err != nil {
			http.Error(w, "bad json", 500)
			return
		}

		// Rewrite absolute image paths to /images/filename
		if imagesDir != "" {
			post.Body = rewriteImagePaths(post.Body, imagesDir)
		}

		// Build comment tree from flat list
		if len(post.Comments) > 0 && !hasChildren(post.Comments) {
			post.Comments = lj.BuildCommentTree(post.Comments)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		postTmpl.Execute(w, &post)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		entries, err := os.ReadDir(*dir)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		var posts []postEntry
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			id, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
			if err != nil {
				continue
			}

			data, err := os.ReadFile(filepath.Join(*dir, name))
			if err != nil {
				continue
			}

			var p struct {
				Title    string         `json:"title"`
				Date     string         `json:"date"`
				Comments []*lj.Comment  `json:"comments,omitempty"`
			}
			if err := json.Unmarshal(data, &p); err != nil {
				log.Printf("Warning: bad json in %s: %v", name, err)
				continue
			}

			posts = append(posts, postEntry{
				ID:           id,
				Title:        p.Title,
				Date:         p.Date,
				CommentCount: countComments(p.Comments),
			})
		}

		sort.Slice(posts, func(i, j int) bool { return posts[i].ID < posts[j].ID })

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		indexTmpl.Execute(w, struct {
			User  string
			Posts []postEntry
		}{user, posts})
	})

	log.Printf("Serving %s on %s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func countComments(comments []*lj.Comment) int {
	n := len(comments)
	for _, c := range comments {
		n += countComments(c.Children)
	}
	return n
}

// hasChildren checks if the comment list is already a tree (has any children).
func hasChildren(comments []*lj.Comment) bool {
	for _, c := range comments {
		if len(c.Children) > 0 {
			return true
		}
	}
	return false
}

// rewriteImagePaths replaces absolute image paths containing imagesDir with /images/filename.
func rewriteImagePaths(body, imagesDir string) string {
	// Replace paths like src="/home/q/galkovsky/images/abc123.jpg" with src="/images/abc123.jpg"
	return strings.ReplaceAll(body, `src="`+imagesDir+`/`, `src="/images/`)
}
