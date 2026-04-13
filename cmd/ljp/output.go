package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/wpt/ljp/pkg/lj"
)

//go:embed post.html
var postTemplateFile embed.FS

var htmlTmpl = template.Must(
	template.New("post.html").Funcs(template.FuncMap{
		"raw": func(s string) template.HTML { return template.HTML(s) },
	}).ParseFS(postTemplateFile, "post.html"),
)

func makePostWriter(dir string, pretty bool, render bool) func(*lj.Post) error {
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return func(p *lj.Post) error { return err }
		}
		ext := ".json"
		if render {
			ext = ".html"
		}
		return func(p *lj.Post) error {
			path := filepath.Join(dir, fmt.Sprintf("%d%s", p.ID, ext))
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if render {
				return renderHTML(f, p)
			}
			enc := json.NewEncoder(f)
			if pretty {
				enc.SetIndent("", "  ")
			}
			enc.SetEscapeHTML(false)
			return enc.Encode(p)
		}
	}
	if render {
		return func(p *lj.Post) error {
			return renderHTML(os.Stdout, p)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return func(p *lj.Post) error {
		return enc.Encode(p)
	}
}

func writePost(post *lj.Post, output string, pretty bool, render bool) {
	var w *os.File
	var err error
	if output != "" {
		w, err = os.Create(output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
			os.Exit(1)
		}
		defer w.Close()
	} else {
		w = os.Stdout
	}

	if render {
		if err := renderHTML(w, post); err != nil {
			fmt.Fprintf(os.Stderr, "Error rendering HTML: %v\n", err)
			os.Exit(1)
		}
	} else {
		enc := json.NewEncoder(w)
		if pretty {
			enc.SetIndent("", "  ")
		}
		enc.SetEscapeHTML(false)
		if err := enc.Encode(post); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
			os.Exit(1)
		}
	}

	if output != "" {
		fmt.Fprintf(os.Stderr, "Written to %s\n", output)
	}
}

func renderHTML(w io.Writer, post *lj.Post) error {
	return htmlTmpl.Execute(w, post)
}

// makeSyncPostWriter wraps makePostWriter with a mutex for concurrent use.
func makeSyncPostWriter(dir string, pretty bool, render bool) func(*lj.Post) error {
	inner := makePostWriter(dir, pretty, render)
	var mu sync.Mutex
	return func(p *lj.Post) error {
		mu.Lock()
		defer mu.Unlock()
		return inner(p)
	}
}
