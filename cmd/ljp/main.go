package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/wpt/ljp/pkg/lj"
	"golang.org/x/sync/errgroup"
)

// version is stamped by the release build (goreleaser -X main.version=...);
// "dev" for plain `go build`/`go install`.
var version = "dev"

// fatalf prints a usage-style error to stderr and exits 1. Used for flag
// validation that happens before the signal context exists.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	comments := flag.Bool("comments", false, "include comments")
	count := flag.Bool("count", false, "show indexable post count and exit")
	first := flag.Bool("first", false, "fetch the oldest post")
	last := flag.Bool("last", false, "fetch the newest post")
	latestWithComments := flag.Int("latest-with-comments", 0, "fetch N newest posts that have comments")
	concurrency := flag.Int("concurrency", lj.HTTPConcurrency, "max concurrent HTTP connections / fan-out width")
	format := flag.String("format", "html", "body format: html, markdown, text")
	images := flag.String("images", "", "download images to this directory")
	resume := flag.Bool("resume", false, "skip already downloaded posts (with --dir)")
	render := flag.Bool("render", false, "output as HTML instead of JSON")
	output := flag.String("o", "", "output file (default: stdout)")
	dir := flag.String("dir", "", "output directory (one file per post)")
	pretty := flag.Bool("pretty", true, "pretty-print JSON")
	verbose := flag.Bool("v", false, "verbose (debug-level) logging")
	showVersion := flag.Bool("version", false, "print version and exit")
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
		fmt.Fprintf(os.Stderr, "  ljp --count news                  (indexable post count)\n")
		fmt.Fprintf(os.Stderr, "  ljp --first news                  (oldest post)\n")
		fmt.Fprintf(os.Stderr, "  ljp --last news                   (newest post)\n")
		fmt.Fprintf(os.Stderr, "  ljp --latest-with-comments 5 --dir ./posts news (5 newest with replies)\n")
		fmt.Fprintf(os.Stderr, "  ljp --comments --dir ./posts news (all to dir)\n")
		fmt.Fprintf(os.Stderr, "  ljp --format markdown news/166511  (body as markdown)\n")
		fmt.Fprintf(os.Stderr, "  ljp --images ./img news/166511     (download images)\n")
		fmt.Fprintf(os.Stderr, "  ljp --resume --dir ./posts news   (skip existing)\n")
		fmt.Fprintf(os.Stderr, "  ljp --render --comments news/166511 (view as HTML)\n")
		fmt.Fprintf(os.Stderr, "  ljp --concurrency 40 --dir ./posts news (tune parallelism)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("ljp", version)
		return
	}

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	user, id, err := parseArg(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	switch *format {
	case lj.FormatHTML, lj.FormatMarkdown, lj.FormatText:
		// ok
	default:
		fmt.Fprintf(os.Stderr, "Error: --format must be html, markdown, or text (got %q)\n", *format)
		os.Exit(1)
	}
	if *concurrency < 1 {
		fmt.Fprintf(os.Stderr, "Error: --concurrency must be >= 1 (got %d)\n", *concurrency)
		os.Exit(1)
	}
	if *latestWithComments < 0 {
		fmt.Fprintf(os.Stderr, "Error: --latest-with-comments must be >= 0 (got %d)\n", *latestWithComments)
		os.Exit(1)
	}
	modeCount := 0
	for _, b := range []bool{*count, *first, *last, *latestWithComments > 0} {
		if b {
			modeCount++
		}
	}
	if modeCount > 1 {
		fatalf("--count, --first, --last, and --latest-with-comments are mutually exclusive")
	}

	selectorArg := flag.NArg() > 1
	// --count/--first/--last take just a username; a selector or explicit post ID
	// alongside them is contradictory (and was previously ignored silently).
	if (*count || *first || *last) && (selectorArg || id != 0) {
		fatalf("--count/--first/--last take only a username, no selector or post ID")
	}

	// singlePostMode is the one path that writes a single post via -o/stdout. All
	// other modes fan out through --dir or a stdout stream.
	singlePostMode := !*count && *latestWithComments == 0 && !selectorArg && (id != 0 || *first || *last)

	if *output != "" && *dir != "" {
		fatalf("use either -o or --dir, not both")
	}
	if *output != "" && !singlePostMode {
		fatalf("-o writes a single file; use --dir for multiple posts")
	}
	if *resume && *dir == "" {
		fatalf("--resume requires --dir (it skips posts already saved there)")
	}
	if *render && *dir == "" && !singlePostMode {
		fatalf("--render with multiple posts needs --dir (one HTML file per post)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := lj.NewClient()
	client.SetConcurrency(*concurrency)
	client.Logger = newCLILogger(*verbose)
	client.BodyFormat = *format
	client.ImagesDir = *images

	if *resume && *dir != "" {
		ids, err := scanExistingPosts(*dir)
		if err != nil {
			exit(ctx, fmt.Errorf("scanning resume dir %s: %w", *dir, err))
		}
		client.SkipIDs = ids
		if len(ids) > 0 {
			fmt.Fprintf(os.Stderr, "Resume: found %d existing posts in %s\n", len(ids), *dir)
		}
	}

	if *count {
		index, err := lj.FetchPostIndex(ctx, client, user)
		if err != nil {
			exit(ctx, err)
		}
		fmt.Println(len(index))
		return
	}

	if *latestWithComments > 0 {
		runLatestWithComments(ctx, client, user, *latestWithComments, *dir, *pretty, *render)
		return
	}

	if *first && id == 0 {
		fmt.Fprintf(os.Stderr, "Finding oldest post...\n")
		id, err = lj.FindFirstPostID(ctx, client, user)
		if err != nil {
			exit(ctx, err)
		}
		fmt.Fprintf(os.Stderr, "Oldest post: %s/%d\n", user, id)
	}

	if *last && id == 0 {
		fmt.Fprintf(os.Stderr, "Finding newest post...\n")
		id, err = lj.FindLastPostID(ctx, client, user)
		if err != nil {
			exit(ctx, err)
		}
		fmt.Fprintf(os.Stderr, "Newest post: %s/%d\n", user, id)
	}

	if id == 0 && flag.NArg() > 1 {
		sel, err := parseSelector(flag.Arg(1))
		if err != nil {
			exit(ctx, err)
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
		exit(ctx, fmt.Errorf("fetching post: %w", err))
	}

	if *comments {
		fmt.Fprintf(os.Stderr, "Fetching comments...\n")
		post.Comments, err = lj.ParseComments(ctx, client, user, id)
		if err != nil {
			exit(ctx, fmt.Errorf("fetching comments: %w", err))
		}
		fmt.Fprintf(os.Stderr, "Got %d comments\n", lj.CountComments(post.Comments))
	}

	if *dir != "" {
		// Honour --dir for a single post too: write {dir}/{id}.json|.html.
		onPost, err := makePostWriter(*dir, *pretty, *render)
		if err != nil {
			exit(ctx, err)
		}
		if err := onPost(post); err != nil {
			exit(ctx, err)
		}
	} else if err := writePost(post, *output, *pretty, *render); err != nil {
		exit(ctx, err)
	}
}

func runSelectionMode(ctx context.Context, client *lj.Client, user string, sel *selector, comments bool, dir string, pretty bool, render bool) {
	ids, err := resolveLJIDs(ctx, client, user, sel)
	if err != nil {
		exit(ctx, err)
	}

	if len(ids) == 0 {
		fmt.Fprintf(os.Stderr, "No posts matched selector\n")
		return
	}

	fmt.Fprintf(os.Stderr, "Fetching %d posts...\n", len(ids))

	filtered := filterSkipped(ids, client.SkipIDs)

	if len(filtered) > 1 {
		runParallel(ctx, client, user, filtered, comments, dir, pretty, render)
	} else {
		onPost, err := makePostWriter(dir, pretty, render)
		if err != nil {
			exit(ctx, err)
		}
		// A single explicitly-selected post: treat a fetch failure as fatal, so
		// `ljp news @missing` exits non-zero like `ljp news/missing` does, rather
		// than printing a warning and exiting 0.
		for _, id := range filtered {
			if err := fetchAndWrite(ctx, client, user, id, comments, true, onPost); err != nil {
				exit(ctx, err)
			}
		}
	}
}

func runLatestWithComments(ctx context.Context, client *lj.Client, user string, n int, dir string, pretty bool, render bool) {
	fmt.Fprintf(os.Stderr, "Building post index...\n")
	index, err := lj.FetchPostIndex(ctx, client, user)
	if err != nil {
		exit(ctx, err)
	}
	if len(index) == 0 {
		fmt.Fprintf(os.Stderr, "No posts found\n")
		return
	}

	onPost, err := makeSyncPostWriter(dir, pretty, render)
	if err != nil {
		exit(ctx, err)
	}
	found := 0
	batchSize := max(1, client.HTTPConcurrency)
	// Walk newest→oldest in batches; inside a batch, fetch post+comments in
	// parallel, then process completions in newest-first order so --resume counts
	// and the "stop at N" cutoff stay deterministic.
	for i := len(index) - 1; i >= 0 && found < n; {
		if ctx.Err() != nil {
			break
		}

		// Collect up to batchSize candidate IDs, skipping resume-counted ones up-front.
		type slot struct {
			id   int
			post *lj.Post
			done bool
		}
		var batch []slot
		// Collect a full concurrency-wide batch even when few posts remain to be
		// found, so the fan-out stays parallel for small N. We overfetch on
		// purpose (many posts have zero comments and don't count); the only stop
		// is the target being met via already-present (resume-skipped) posts.
		for len(batch) < batchSize && i >= 0 && found < n {
			id := index[i]
			i--
			if client.SkipIDs[id] {
				// Already present on disk — count toward target without refetch.
				// Resume summary was printed once at startup; no per-id chatter.
				found++
				continue
			}
			batch = append(batch, slot{id: id})
		}
		if len(batch) == 0 {
			continue
		}

		eg, ectx := errgroup.WithContext(ctx)
		eg.SetLimit(batchSize)
		for j := range batch {
			eg.Go(func() error {
				id := batch[j].id
				post, err := lj.ParsePost(ectx, client, user, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: post %d: %v\n", id, err)
					return nil
				}
				// Always fetch comments — cheap (single empty page) when none exist.
				post.Comments, err = lj.ParseComments(ectx, client, user, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: comments for %d: %v\n", id, err)
				}
				batch[j].post = post
				batch[j].done = true
				return nil
			})
		}
		_ = eg.Wait()

		// Process in newest-first order.
		for _, s := range batch {
			if found >= n {
				break
			}
			if !s.done || s.post == nil {
				continue
			}
			if len(s.post.Comments) == 0 {
				continue
			}
			if err := onPost(s.post); err != nil {
				exit(ctx, fmt.Errorf("writing post %d: %w", s.id, err))
			}
			found++
			fmt.Fprintf(os.Stderr, "Progress: %d/%d (post %d, %d comments)\n", found, n, s.id, lj.CountComments(s.post.Comments))
		}
	}

	// Distinguish "walked the whole journal, came up short" from "interrupted":
	// a signal-cancelled context must exit 130, not report a benign shortfall.
	if ctx.Err() != nil {
		exit(ctx, ctx.Err())
	}
	if found < n {
		fmt.Fprintf(os.Stderr, "Only found %d posts with comments (requested %d)\n", found, n)
	}
}

func runJournalMode(ctx context.Context, client *lj.Client, user string, comments bool, dir string, pretty bool, render bool) {
	fmt.Fprintf(os.Stderr, "Fetching journal %s...\n", user)

	// Full index via calendar pages, then parallel fetch with HTTPConcurrency.
	ids, err := lj.FetchFullPostIndex(ctx, client, user)
	if err != nil {
		exit(ctx, err)
	}
	filtered := filterSkipped(ids, client.SkipIDs)
	fmt.Fprintf(os.Stderr, "Fetching %d posts (concurrency=%d)...\n", len(filtered), client.HTTPConcurrency)
	runParallel(ctx, client, user, filtered, comments, dir, pretty, render)
}

// fetchAndWrite fetches a post (and optionally comments) and writes it. A fetch
// failure is normally a per-post warning (skip and continue the bulk run), but
// context cancellation is always propagated (so SIGINT exits 130, not 0), and
// fatalOnError makes any fetch failure fatal — used when the user named one
// specific post, so a 404 is an error rather than a silently empty success.
func fetchAndWrite(ctx context.Context, client *lj.Client, user string, id int, comments, fatalOnError bool, onPost func(*lj.Post) error) error {
	fmt.Fprintf(os.Stderr, "Fetching post %s/%d...\n", user, id)
	post, err := lj.ParsePost(ctx, client, user, id)
	if err != nil {
		if fatalOnError || ctx.Err() != nil {
			return fmt.Errorf("post %d: %w", id, err)
		}
		fmt.Fprintf(os.Stderr, "Warning: post %d: %v\n", id, err)
		return nil // skip post, not fatal
	}

	if comments {
		post.Comments, err = lj.ParseComments(ctx, client, user, id)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("comments for %d: %w", id, err)
			}
			fmt.Fprintf(os.Stderr, "Warning: comments for %d: %v\n", id, err)
		}
	}

	if err := onPost(post); err != nil {
		return fmt.Errorf("writing post %d: %w", id, err)
	}
	return nil
}

func runParallel(ctx context.Context, client *lj.Client, user string, ids []int, comments bool, dir string, pretty bool, render bool) {
	workers := max(1, min(client.HTTPConcurrency, len(ids)))
	onPost, err := makeSyncPostWriter(dir, pretty, render)
	if err != nil {
		exit(ctx, err)
	}

	var done atomic.Int64
	total := len(ids)

	// Don't shadow ctx — we still need the original signal-cancellable parent
	// for exit() so a worker error doesn't look like a signal cancellation.
	eg, ectx := errgroup.WithContext(ctx)
	eg.SetLimit(workers)
	for _, id := range ids {
		if ectx.Err() != nil {
			break
		}
		eg.Go(func() error {
			if err := fetchAndWrite(ectx, client, user, id, comments, false, onPost); err != nil {
				return err
			}
			n := done.Add(1)
			fmt.Fprintf(os.Stderr, "Progress: %d/%d\n", n, total)
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		exit(ctx, err)
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
	id, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("invalid post ID: %s", parts[1])
	}
	// id 0 is the "whole journal" sentinel (user with no /id); an explicit
	// user/0 or user/-5 is a malformed ID, not a request to archive everything.
	if id <= 0 {
		return "", 0, fmt.Errorf("invalid post ID: %s", parts[1])
	}
	return parts[0], id, nil
}

func filterSkipped(ids []int, skipIDs map[int]bool) []int {
	if len(skipIDs) == 0 {
		return ids
	}
	filtered := make([]int, 0, len(ids))
	skipped := 0
	for _, id := range ids {
		if skipIDs[id] {
			skipped++
			continue
		}
		filtered = append(filtered, id)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "Skipping %d posts already present\n", skipped)
	}
	return filtered
}

// exit prints err and terminates. If ctx was cancelled by an OS signal (via
// signal.NotifyContext), the signal cause is shown instead and the process exits
// 130 — the shell convention for SIGINT (128 + signum).
func exit(ctx context.Context, err error) {
	if cause := context.Cause(ctx); cause != nil &&
		!errors.Is(cause, context.Canceled) &&
		!errors.Is(cause, context.DeadlineExceeded) {
		fmt.Fprintf(os.Stderr, "%v\n", cause)
		os.Exit(130)
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}

// newCLILogger configures slog for CLI use: text output to stderr, no timestamps.
func newCLILogger(verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// scanExistingPosts reads a directory for {id}.json or {id}.html files and
// returns their IDs. Empty files (e.g. from a crashed run) and subdirectories
// are ignored. A missing directory yields an empty map and no error; any other
// ReadDir failure is reported so the caller can decide whether to bail.
func scanExistingPosts(dir string) (map[int]bool, error) {
	ids := make(map[int]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := filepath.Ext(name)
		if ext != ".json" && ext != ".html" {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSuffix(name, ext))
		if err != nil || id <= 0 {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			// Treat 0-byte files (crashed mid-write) as not-yet-downloaded.
			continue
		}
		ids[id] = true
	}
	return ids, nil
}
