package main

import (
	"cmp"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
		handlePost(w, r, *dir, imagesDir)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleIndex(w, r, *dir, user)
	})

	log.Printf("Serving %s on %s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func handlePost(w http.ResponseWriter, r *http.Request, dir, imagesDir string) {
	idStr := strings.TrimPrefix(r.URL.Path, "/post/")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid post ID", 400)
		return
	}

	data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%d.json", id)))
	if err != nil {
		http.Error(w, "post not found", 404)
		return
	}

	var post lj.Post
	if err := json.Unmarshal(data, &post); err != nil {
		log.Printf("Error: bad json in %d.json: %v", id, err)
		http.Error(w, "bad json", 500)
		return
	}

	if imagesDir != "" {
		post.Body = rewriteImagePaths(post.Body, imagesDir)
	}

	// Build comment tree from flat list
	if len(post.Comments) > 0 && !hasChildren(post.Comments) {
		post.Comments = lj.BuildCommentTree(post.Comments)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := postTmpl.Execute(w, &post); err != nil {
		log.Printf("Error: rendering post %d: %v", id, err)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request, dir, user string) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	entries, err := os.ReadDir(dir)
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

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		var p struct {
			Title    string        `json:"title"`
			Date     string        `json:"date"`
			Comments []*lj.Comment `json:"comments,omitempty"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			log.Printf("Warning: bad json in %s: %v", name, err)
			continue
		}

		posts = append(posts, postEntry{
			ID:           id,
			Title:        p.Title,
			Date:         p.Date,
			CommentCount: lj.CountComments(p.Comments),
		})
	}

	slices.SortFunc(posts, func(a, b postEntry) int { return cmp.Compare(a.ID, b.ID) })

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, struct {
		User  string
		Posts []postEntry
	}{user, posts}); err != nil {
		log.Printf("Error: rendering index: %v", err)
	}
}

func hasChildren(comments []*lj.Comment) bool {
	return slices.ContainsFunc(comments, func(c *lj.Comment) bool {
		return len(c.Children) > 0
	})
}

// rewriteImagePaths replaces absolute image paths containing imagesDir with /images/filename.
func rewriteImagePaths(body, imagesDir string) string {
	return strings.ReplaceAll(body, `src="`+imagesDir+`/`, `src="/images/`)
}
