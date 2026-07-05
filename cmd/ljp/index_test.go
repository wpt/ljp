package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteArchiveIndex(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("12.html", `<!DOCTYPE html><html><head><title>Hello &amp; goodbye</title></head><body>x</body></html>`)
	write("5.html", `<!DOCTYPE html><html><head><title>#5</title></head><body>x</body></html>`)
	write("99.html", "") // 0-byte leftover from a crashed run — must be skipped
	write("007.html", `<!DOCTYPE html><html><head><title>Padded</title></head><body>x</body></html>`)
	write("notes.txt", "not a post")

	if err := writeArchiveIndex(dir); err != nil {
		t.Fatalf("writeArchiveIndex: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	idx := string(data)

	// Title is unescaped from the post file and re-escaped by the template —
	// exactly once (no &amp;amp;).
	if !strings.Contains(idx, `<a href="12.html">Hello &amp; goodbye</a>`) {
		t.Errorf("index missing post 12 link: %s", idx)
	}
	if strings.Contains(idx, "&amp;amp;") {
		t.Errorf("double-escaped title: %s", idx)
	}
	if !strings.Contains(idx, `<a href="5.html">#5</a>`) {
		t.Errorf("index missing post 5 link: %s", idx)
	}
	if strings.Contains(idx, "99.html") {
		t.Errorf("0-byte post must not be listed: %s", idx)
	}
	if strings.Contains(idx, "notes") {
		t.Errorf("non-post file must not be listed: %s", idx)
	}
	// The href must be the real filename, not the parsed ID re-rendered —
	// "007.html" linked as "7.html" would 404.
	if !strings.Contains(idx, `<a href="007.html">Padded</a>`) {
		t.Errorf("non-canonical filename must keep its real name as href: %s", idx)
	}
	// Newest (highest ID) first.
	if strings.Index(idx, "12.html") > strings.Index(idx, `"5.html"`) {
		t.Errorf("posts not sorted newest-first: %s", idx)
	}
	if !strings.Contains(idx, "3 posts") {
		t.Errorf("want '3 posts' count, got: %s", idx)
	}

	// Regenerating over an existing index must not list index.html itself.
	if err := writeArchiveIndex(dir); err != nil {
		t.Fatalf("second writeArchiveIndex: %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "index.html"))
	if strings.Contains(string(data), `href="index.html"`) {
		t.Error("index.html listed itself")
	}
	if !strings.Contains(string(data), "3 posts") {
		t.Errorf("regenerated index lost entries: %s", string(data))
	}
}

func TestWriteArchiveIndexTitleFallback(t *testing.T) {
	dir := t.TempDir()
	// A post file with no <title> in its head — falls back to #{id}.
	if err := os.WriteFile(filepath.Join(dir, "7.html"), []byte("<html><body>no title</body></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeArchiveIndex(dir); err != nil {
		t.Fatalf("writeArchiveIndex: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	if !strings.Contains(string(data), `<a href="7.html">#7</a>`) {
		t.Errorf("missing fallback title link: %s", string(data))
	}
}
