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
		fmt.Fprintf(os.Stderr, "  ljp --render --comments news/166511 (view as HTML)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

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
		runSelectionMode(ctx, client, user, sel, *comments, *dir, *pretty, *render)
		return
	}

	if id == 0 {
		runJournalMode(ctx, client, user, *comments, *dir, *pretty, *render)
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
		total := countComments(post.Comments)
		fmt.Fprintf(os.Stderr, "Got %d comments\n", total)
	}

	writePost(post, *output, *pretty, *render)
}

func runSelectionMode(ctx context.Context, client *lj.Client, user string, sel *selector, comments bool, dir string, pretty bool, render bool) {
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
	onPost := makePostWriter(dir, pretty, render)

	for _, id := range ids {
		if client.SkipIDs != nil && client.SkipIDs[id] {
			fmt.Fprintf(os.Stderr, "Skipping post %d (already exists)\n", id)
			continue
		}

		fmt.Fprintf(os.Stderr, "Fetching post %s/%d...\n", user, id)
		post, err := lj.ParsePost(ctx, client, user, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post %d: %v\n", id, err)
			continue
		}

		if comments {
			post.Comments, err = lj.ParseComments(ctx, client, user, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: comments for %d: %v\n", id, err)
			}
		}

		if err := onPost(post); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing post: %v\n", err)
			os.Exit(1)
		}
	}
}

func runJournalMode(ctx context.Context, client *lj.Client, user string, comments bool, dir string, pretty bool, render bool) {
	onPost := makePostWriter(dir, pretty, render)

	fmt.Fprintf(os.Stderr, "Fetching journal %s...\n", user)
	if err := lj.ParseJournal(ctx, client, user, comments, onPost); err != nil {
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

func countComments(comments []*lj.Comment) int {
	n := len(comments)
	for _, c := range comments {
		n += countComments(c.Children)
	}
	return n
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
