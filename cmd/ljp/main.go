package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wpt/ljp/pkg/lj"
)

func main() {
	comments := flag.Bool("comments", false, "include comments")
	count := flag.Bool("count", false, "show journal entry count and exit")
	first := flag.Bool("first", false, "fetch the oldest post")
	last := flag.Bool("last", false, "fetch the newest post")
	format := flag.String("format", "html", "body format: html, markdown, text")
	images := flag.String("images", "", "download images to this directory")
	resume := flag.Bool("resume", false, "skip already downloaded posts (with --dir)")
	workers := flag.Int("workers", 4, "number of parallel workers (max 8)")
	render := flag.Bool("render", false, "output as HTML instead of JSON")
	output := flag.String("o", "", "output file (default: stdout)")
	dir := flag.String("dir", "", "output directory (one file per post)")
	pretty := flag.Bool("pretty", true, "pretty-print JSON")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ljp [flags] <username> [selector]\n\n")
		fmt.Fprintf(os.Stderr, "Selectors:\n")
		fmt.Fprintf(os.Stderr, "  (none)          all posts\n")
		fmt.Fprintf(os.Stderr, "  1-222           ordinal range (1 = oldest)\n")
		fmt.Fprintf(os.Stderr, "  1,33,444        ordinal list\n")
		fmt.Fprintf(os.Stderr, "  @166511          LJ post ID\n")
		fmt.Fprintf(os.Stderr, "  @256,@166511     LJ ID list\n")
		fmt.Fprintf(os.Stderr, "  @256-@100000    LJ ID range\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  ljp news                          (all posts, JSONL)\n")
		fmt.Fprintf(os.Stderr, "  ljp --comments news 1-10          (first 10 with comments)\n")
		fmt.Fprintf(os.Stderr, "  ljp news @166511                   (single post by LJ ID)\n")
		fmt.Fprintf(os.Stderr, "  ljp news/166511                    (same, old syntax)\n")
		fmt.Fprintf(os.Stderr, "  ljp --count news                  (entry count)\n")
		fmt.Fprintf(os.Stderr, "  ljp --first news                  (oldest post)\n")
		fmt.Fprintf(os.Stderr, "  ljp --last news                   (newest post)\n")
		fmt.Fprintf(os.Stderr, "  ljp --comments --dir ./posts news (all to dir)\n")
		fmt.Fprintf(os.Stderr, "  ljp --format markdown news/166511  (body as markdown)\n")
		fmt.Fprintf(os.Stderr, "  ljp --images ./img news/166511     (download images)\n")
		fmt.Fprintf(os.Stderr, "  ljp --resume --dir ./posts news   (skip existing)\n")
		fmt.Fprintf(os.Stderr, "  ljp --render --comments news/166511 (view as HTML)\n")
		fmt.Fprintf(os.Stderr, "  ljp --workers 4 --comments --dir ./posts news (parallel)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	*workers = max(1, min(*workers, 8))

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	user, id, err := parseArg(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client := lj.NewClient()
	client.Log = func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
	}
	client.BodyFormat = *format
	client.ImagesDir = *images

	if *resume && *dir != "" {
		client.SkipIDs = scanExistingPosts(*dir)
		if len(client.SkipIDs) > 0 {
			fmt.Fprintf(os.Stderr, "Resume: found %d existing posts in %s\n", len(client.SkipIDs), *dir)
		}
	}

	if *count {
		n, err := lj.ParseProfileStats(ctx, client, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(n)
		return
	}

	if *first && id == 0 {
		fmt.Fprintf(os.Stderr, "Finding oldest post...\n")
		id, err = lj.FindFirstPostID(ctx, client, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Oldest post: %s/%d\n", user, id)
	}

	if *last && id == 0 {
		fmt.Fprintf(os.Stderr, "Finding newest post...\n")
		id, err = lj.FindLastPostID(ctx, client, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Newest post: %s/%d\n", user, id)
	}

	if id == 0 && flag.NArg() > 1 {
		sel, err := parseSelector(flag.Arg(1))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		runSelectionMode(ctx, client, user, sel, *comments, *dir, *pretty, *render, *workers)
		return
	}

	if id == 0 {
		runJournalMode(ctx, client, user, *comments, *dir, *pretty, *render, *workers)
		return
	}

	fmt.Fprintf(os.Stderr, "Fetching post %s/%d...\n", user, id)
	post, err := lj.ParsePost(ctx, client, user, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching post: %v\n", err)
		os.Exit(1)
	}

	if *comments {
		fmt.Fprintf(os.Stderr, "Fetching comments...\n")
		post.Comments, err = lj.ParseComments(ctx, client, user, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching comments: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Got %d comments\n", lj.CountComments(post.Comments))
	}

	writePost(post, *output, *pretty, *render)
}

func runSelectionMode(ctx context.Context, client *lj.Client, user string, sel *selector, comments bool, dir string, pretty bool, render bool, workers int) {
	ids, err := resolveLJIDs(ctx, client, user, sel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(ids) == 0 {
		fmt.Fprintf(os.Stderr, "No posts matched selector\n")
		return
	}

	fmt.Fprintf(os.Stderr, "Fetching %d posts...\n", len(ids))

	filtered := filterSkipped(ids, client.SkipIDs)

	if workers > 1 && len(filtered) > 1 {
		runParallel(ctx, client, user, filtered, comments, dir, pretty, render, workers)
	} else {
		onPost := makePostWriter(dir, pretty, render)
		for _, id := range filtered {
			if err := fetchAndWrite(ctx, client, user, id, comments, onPost); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	}
}

func runJournalMode(ctx context.Context, client *lj.Client, user string, comments bool, dir string, pretty bool, render bool, workers int) {
	fmt.Fprintf(os.Stderr, "Fetching journal %s...\n", user)

	if workers > 1 {
		// Full index via calendar pages, then parallel fetch
		ids, err := lj.FetchFullPostIndex(ctx, client, user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		filtered := filterSkipped(ids, client.SkipIDs)

		fmt.Fprintf(os.Stderr, "Fetching %d posts with %d workers...\n", len(filtered), workers)
		runParallel(ctx, client, user, filtered, comments, dir, pretty, render, workers)
		return
	}

	onPost := makePostWriter(dir, pretty, render)
	if err := lj.ParseJournal(ctx, client, user, comments, onPost); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func fetchAndWrite(ctx context.Context, client *lj.Client, user string, id int, comments bool, onPost func(*lj.Post) error) error {
	fmt.Fprintf(os.Stderr, "Fetching post %s/%d...\n", user, id)
	post, err := lj.ParsePost(ctx, client, user, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post %d: %v\n", id, err)
		return nil // skip post, not fatal
	}

	if comments {
		post.Comments, err = lj.ParseComments(ctx, client, user, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: comments for %d: %v\n", id, err)
		}
	}

	if err := onPost(post); err != nil {
		return fmt.Errorf("writing post %d: %w", id, err)
	}
	return nil
}

// newWorkerClient creates a new Client with the same config as the original.
func newWorkerClient(src *lj.Client) *lj.Client {
	c := lj.NewClient()
	c.Log = src.Log
	c.BodyFormat = src.BodyFormat
	c.ImagesDir = src.ImagesDir
	c.SkipIDs = src.SkipIDs
	return c
}

func runParallel(ctx context.Context, client *lj.Client, user string, ids []int, comments bool, dir string, pretty bool, render bool, workers int) {
	workers = min(workers, len(ids))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	onPost := makeSyncPostWriter(dir, pretty, render)
	ch := make(chan int, len(ids))
	for _, id := range ids {
		ch <- id
	}
	close(ch)

	var done atomic.Int64
	total := len(ids)
	var firstErr atomic.Value

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		wc := newWorkerClient(client)
		go func() {
			defer wg.Done()
			for id := range ch {
				if ctx.Err() != nil {
					return
				}
				if err := fetchAndWrite(ctx, wc, user, id, comments, onPost); err != nil {
					firstErr.CompareAndSwap(nil, err)
					cancel()
					return
				}
				n := done.Add(1)
				fmt.Fprintf(os.Stderr, "Progress: %d/%d\n", n, total)
			}
		}()
	}
	wg.Wait()

	if err, ok := firstErr.Load().(error); ok && err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func parseArg(arg string) (string, int, error) {
	if strings.Contains(arg, "livejournal.com") {
		return lj.ParsePostURL(arg)
	}
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) == 1 {
		return parts[0], 0, nil
	}
	id := 0
	_, err := fmt.Sscanf(parts[1], "%d", &id)
	if err != nil {
		return "", 0, fmt.Errorf("invalid post ID: %s", parts[1])
	}
	return parts[0], id, nil
}

func filterSkipped(ids []int, skipIDs map[int]bool) []int {
	if len(skipIDs) == 0 {
		return ids
	}
	filtered := make([]int, 0, len(ids))
	for _, id := range ids {
		if skipIDs[id] {
			fmt.Fprintf(os.Stderr, "Skipping post %d (already exists)\n", id)
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered
}

// scanExistingPosts reads a directory for {id}.json files and returns their IDs.
func scanExistingPosts(dir string) map[int]bool {
	ids := make(map[int]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ids
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".json" {
			if id, err := strconv.Atoi(strings.TrimSuffix(name, ".json")); err == nil {
				ids[id] = true
			}
		}
	}
	return ids
}
