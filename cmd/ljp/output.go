package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/wpt/ljp/pkg/lj"
)

// newJSONEncoder builds the encoder used everywhere the CLI emits post JSON:
// HTML chars stay unescaped (post bodies contain raw '<', '>'; downstream
// consumers must re-escape before embedding in HTML), indent when pretty.
func newJSONEncoder(w io.Writer, pretty bool) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc
}

// makePostWriter returns a writer closure for the current output mode.
// Construction-time errors (e.g. MkdirAll failure) are returned now, not
// silently captured into every per-post call.
func makePostWriter(dir string, pretty bool, render bool) (func(*lj.Post) error, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("creating output dir: %w", err)
		}
		ext := ".json"
		if render {
			ext = ".html"
		}
		return func(p *lj.Post) error {
			return writePostFile(dir, ext, p, pretty, render)
		}, nil
	}
	if render {
		return func(p *lj.Post) error {
			return lj.RenderPost(os.Stdout, p)
		}, nil
	}
	enc := newJSONEncoder(os.Stdout, pretty)
	return func(p *lj.Post) error {
		return enc.Encode(p)
	}, nil
}

// writeFileAtomic writes to final via a sibling .tmp file + rename, so a
// crashed/cancelled run can't leave a half-written file at final. Shared by
// the per-post writers and the archive index writer so both get identical
// crash-atomicity. Not fsynced (program-crash atomicity only, not power-loss
// durability).
func writeFileAtomic(final string, write func(io.Writer) error) error {
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	writeErr := write(f)
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	return os.Rename(tmp, final)
}

// writePostFile writes a single post to {dir}/{id}{ext} atomically, so a
// half-written file can't block --resume.
func writePostFile(dir, ext string, p *lj.Post, pretty, render bool) error {
	final := filepath.Join(dir, fmt.Sprintf("%d%s", p.ID, ext))
	return writeFileAtomic(final, func(w io.Writer) error {
		if render {
			return lj.RenderPost(w, p)
		}
		return newJSONEncoder(w, pretty).Encode(p)
	})
}

func writePost(post *lj.Post, output string, pretty bool, render bool) error {
	if output == "" {
		if render {
			return lj.RenderPost(os.Stdout, post)
		}
		return newJSONEncoder(os.Stdout, pretty).Encode(post)
	}

	f, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	var encErr error
	if render {
		encErr = lj.RenderPost(f, post)
	} else {
		encErr = newJSONEncoder(f, pretty).Encode(post)
	}
	closeErr := f.Close()
	if encErr != nil {
		return fmt.Errorf("writing %s: %w", output, encErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %s: %w", output, closeErr)
	}
	fmt.Fprintf(os.Stderr, "Written to %s\n", output)
	return nil
}

// makeSyncPostWriter wraps makePostWriter with a mutex for concurrent use.
func makeSyncPostWriter(dir string, pretty bool, render bool) (func(*lj.Post) error, error) {
	inner, err := makePostWriter(dir, pretty, render)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	return func(p *lj.Post) error {
		mu.Lock()
		defer mu.Unlock()
		return inner(p)
	}, nil
}
