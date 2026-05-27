package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/wpt/ljp/pkg/lj"
)

// captureStdout swaps os.Stdout for an os.Pipe for the duration of fn and
// returns whatever fn wrote.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()

	fn()
	_ = w.Close()
	return <-done
}

func TestMakePostWriter_DirJSON(t *testing.T) {
	dir := t.TempDir()
	writer, err := makePostWriter(dir, true, false)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}
	p := &lj.Post{ID: 42, Title: "hi", Body: "<b>bold</b>"}
	if err := writer(p); err != nil {
		t.Fatalf("writer: %v", err)
	}

	path := filepath.Join(dir, "42.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var got lj.Post
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != 42 || got.Title != "hi" || got.Body != "<b>bold</b>" {
		t.Errorf("got %+v", got)
	}
	// pretty=true should produce an indented document.
	if !bytes.Contains(data, []byte("\n  ")) {
		t.Errorf("expected pretty-printed output, got %q", data)
	}
	// EscapeHTML(false) — raw '<' must survive.
	if !bytes.Contains(data, []byte("<b>bold</b>")) {
		t.Errorf("expected unescaped HTML in JSON, got %q", data)
	}
}

func TestMakePostWriter_DirRender(t *testing.T) {
	dir := t.TempDir()
	writer, err := makePostWriter(dir, false, true)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}
	p := &lj.Post{ID: 7, Title: "hello", Body: "<p>body</p>"}
	if err := writer(p); err != nil {
		t.Fatalf("writer: %v", err)
	}
	path := filepath.Join(dir, "7.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(data, []byte("<html")) && !bytes.Contains(data, []byte("<!DOCTYPE")) {
		t.Errorf("expected HTML document, got %q", data[:min(len(data), 200)])
	}
}

func TestMakePostWriter_DirAtomicRename(t *testing.T) {
	dir := t.TempDir()
	writer, err := makePostWriter(dir, false, false)
	if err != nil {
		t.Fatalf("makePostWriter: %v", err)
	}
	if err := writer(&lj.Post{ID: 1}); err != nil {
		t.Fatalf("writer: %v", err)
	}
	// No .tmp leftover after successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("unexpected leftover tmp file: %s", e.Name())
		}
	}
}

func TestMakePostWriter_StdoutJSONL(t *testing.T) {
	posts := []*lj.Post{{ID: 1, Title: "a"}, {ID: 2, Title: "b"}}
	out := captureStdout(t, func() {
		writer, err := makePostWriter("", false, false)
		if err != nil {
			t.Fatalf("makePostWriter: %v", err)
		}
		for _, p := range posts {
			if err := writer(p); err != nil {
				t.Fatalf("writer: %v", err)
			}
		}
	})
	lines := bytes.Split(bytes.TrimRight(out, "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d: %q", len(lines), out)
	}
	for i, line := range lines {
		var got lj.Post
		if err := json.Unmarshal(line, &got); err != nil {
			t.Errorf("line %d not valid JSON: %v (%q)", i, err, line)
		}
	}
}

func TestMakePostWriter_StdoutPrettyHonored(t *testing.T) {
	// Regression: --pretty used to silently no-op on stdout. With one post the
	// output should be indented (multi-line) even on stdout.
	out := captureStdout(t, func() {
		writer, err := makePostWriter("", true, false)
		if err != nil {
			t.Fatalf("makePostWriter: %v", err)
		}
		if err := writer(&lj.Post{ID: 1, Title: "x"}); err != nil {
			t.Fatalf("writer: %v", err)
		}
	})
	if !bytes.Contains(out, []byte("\n  ")) {
		t.Errorf("--pretty should indent stdout output, got %q", out)
	}
}

func TestMakePostWriter_MkdirAllFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows file-as-dir-parent semantics differ; skip rather than fight it.
		t.Skip("path-as-file collision behaves differently on Windows")
	}
	parent := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(parent, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	bad := filepath.Join(parent, "child") // cannot mkdir under a regular file
	_, err := makePostWriter(bad, false, false)
	if err == nil {
		t.Errorf("expected construction error for unmakeable dir %q", bad)
	}
}

func TestMakeSyncPostWriter_Concurrent(t *testing.T) {
	dir := t.TempDir()
	writer, err := makeSyncPostWriter(dir, false, false)
	if err != nil {
		t.Fatalf("makeSyncPostWriter: %v", err)
	}
	var wg sync.WaitGroup
	const N = 20
	for i := 1; i <= N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writer(&lj.Post{ID: i, Title: "t"}); err != nil {
				t.Errorf("writer(%d): %v", i, err)
			}
		}()
	}
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonCount++
		}
	}
	if jsonCount != N {
		t.Errorf("got %d .json files, want %d", jsonCount, N)
	}
}

func TestMakeSyncPostWriter_MkdirAllFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path-as-file collision behaves differently on Windows")
	}
	parent := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(parent, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	bad := filepath.Join(parent, "child")
	w, err := makeSyncPostWriter(bad, false, false)
	if err == nil {
		t.Errorf("expected construction error from sync wrapper, got nil")
	}
	if w != nil {
		t.Errorf("expected nil writer on construction failure")
	}
}

func TestWritePost_ToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	p := &lj.Post{ID: 99, Title: "t"}
	// Redirect Stderr to swallow the "Written to" line.
	origErr := os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("devnull: %v", err)
	}
	os.Stderr = devnull
	defer func() { os.Stderr = origErr; devnull.Close() }()

	if err := writePost(p, path, true, false); err != nil {
		t.Fatalf("writePost: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got lj.Post
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != 99 || got.Title != "t" {
		t.Errorf("got %+v", got)
	}
}
