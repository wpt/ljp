# ljp

[![CI](https://github.com/wpt/ljp/actions/workflows/ci.yml/badge.svg)](https://github.com/wpt/ljp/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/wpt/ljp/pkg/lj.svg)](https://pkg.go.dev/github.com/wpt/ljp/pkg/lj)
[![Go Report Card](https://goreportcard.com/badge/github.com/wpt/ljp)](https://goreportcard.com/report/github.com/wpt/ljp)

Archive public LiveJournal journals — posts, threaded comments, and inline images — to local JSON or browsable HTML. No login required. Resumable. Fetches in parallel (40 connections by default). Useful for backing up a journal before deletion, offline reading, or text analysis.

Ships a CLI plus a Go library:

- **`ljp`** — fetcher / CLI
- **`pkg/lj`** — the underlying library

## Install

```bash
go install github.com/wpt/ljp/cmd/ljp@latest
```

Or grab a prebuilt binary from the [Releases page](https://github.com/wpt/ljp/releases) (linux/darwin/windows on amd64/arm64).

## Usage

In examples below, `news` is a LiveJournal username (the official news journal) — substitute any public journal.

```bash
# Single post (by ID or URL)
ljp news/166511
ljp https://news.livejournal.com/166511.html

# With comments, saved to a file
ljp --comments -o post.json news/166511

# Entire journal (JSONL to stdout)
ljp news

# Post selection (ordinal: 1 = oldest)
ljp news 1-10                    # first 10 posts
ljp news 1,5,100                 # specific posts
ljp news @166511                 # by LJ ID
ljp news @256,@166511            # multiple LJ IDs
ljp news @256-@100000            # LJ ID range

# Journal info
ljp --count news                 # indexable post count
ljp --first news                 # oldest post (exponential + binary search)
ljp --last news                  # newest post

# Bulk archive — typical workflow
ljp --comments --images ./img --dir ./posts news     # posts + comments + local images
ljp --resume   --dir ./posts news                    # resume an interrupted run
ljp --latest-with-comments 5 --dir ./posts news      # 5 newest posts that have replies
ljp --concurrency 20 --dir ./posts news              # gentler parallelism (default 40)

# Format & images
ljp --format markdown news/166511                    # body as Markdown
ljp --format text news/166511                        # body as plain text
ljp --images ./img news/166511                       # download images, rewrite <img src>

# Render as a styled, self-contained HTML page with threaded comments
ljp --render --comments -o post.html news/166511
ljp --render --comments --dir ./posts news           # one {id}.html per post

# Verbose (debug) logging to stderr
ljp -v news/166511
```

### Flags

| Flag | Description |
|------|-------------|
| `--comments` | Include comments |
| `--count` | Show indexable post count and exit |
| `--first` | Fetch the oldest post (HEAD-probe + binary search) |
| `--last` | Fetch the newest post |
| `--latest-with-comments <N>` | Fetch the N newest posts that have at least one comment |
| `--format html\|markdown\|text` | Body format (default: html) |
| `--images <dir>` | Download images to directory and rewrite `<img src>` to local paths |
| `--render` | Output as a self-contained HTML page (use with `-o` or `--dir`) |
| `--resume` | Skip posts already in `--dir` (matches `{id}.json` or `{id}.html`) |
| `--concurrency <N>` | Max concurrent HTTP connections / fan-out width (default 40) |
| `--pretty` | Pretty-print JSON (default true; pass `--pretty=false` for compact) |
| `-o <file>` | Output to file (default: stdout) |
| `--dir <dir>` | Output directory (one `{id}.json` or `{id}.html` per post) |
| `-v` | Verbose (debug-level) logging to stderr |
| `--version` | Print version and exit |

Run `ljp -h` for the full list.

When combining `--render`, `--dir`, and `--images`, run your browser from the
working directory you invoked `ljp` in — downloaded `<img src>` paths are written
relative to that directory, not to each `{id}.html` file.

## Output

Single post: pretty JSON to stdout. Multiple posts (without `--dir`): JSONL — pretty-printed by default; pass `--pretty=false` for one compact object per line. With `--dir`, each post becomes `{dir}/{id}.json` (or `{id}.html` with `--render`).

```json
{
  "id": 166511,
  "url": "https://news.livejournal.com/166511.html",
  "title": "Post Title",
  "date": "December 17 2024, 16:01",
  "date_unix": 1734451260,
  "author": "news",
  "body": "<p>Post content...</p>",
  "tags": ["updates"],
  "comments": [
    {
      "id": 10193184,
      "talk_id": 10193184,
      "parent_id": 0,
      "level": 1,
      "author": "User Display Name",
      "username": "user_login",
      "date": "December 17 2024, 17:02",
      "date_unix": 1734454920,
      "body": "Comment text..."
    }
  ]
}
```

`children` appears only on parents whose replies you also fetched; Post-level
`reply_count` and `og` and Comment fields `subject`, `userpic`, `deleted` are
omitted when zero/empty.

## Library

```go
import (
    "context"
    "log"
    "log/slog"
    "os"

    "github.com/wpt/ljp/pkg/lj"
)

client := lj.NewClient()
// Optional: structured progress logging. nil (default) is silent.
client.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
// Options — set BEFORE the first fetch; they're read inside ParsePost.
client.BodyFormat = lj.FormatMarkdown // FormatHTML (default), FormatMarkdown, FormatText
client.ImagesDir = "./images"         // download images locally
ctx := context.Background()

// Single post + comments
post, err := lj.ParsePost(ctx, client, "news", 166511)
if err != nil {
    log.Fatal(err)
}
post.Comments, err = lj.ParseComments(ctx, client, "news", 166511)
if err != nil {
    log.Fatal(err)
}
_ = post // use post.Title, post.Body, post.Comments, ...

// Journal operations
_, _ = lj.FindFirstPostID(ctx, client, "news") // oldest post (HEAD-probe + binary search)
_, _ = lj.FindLastPostID(ctx, client, "news")  // newest post
_, _ = lj.FetchPostIndex(ctx, client, "news")  // ?skip= pages, fast but caps at LJ's index limit
_, _ = lj.FetchFullPostIndex(ctx, client, "news") // monthly archives, exhaustive

// Stream all posts with a callback (sequential — for parallel use FetchFullPostIndex + ParsePost yourself).
if err := lj.ParseJournal(ctx, client, "news", true, func(p *lj.Post) error {
    // process each post
    return nil
}); err != nil {
    log.Fatal(err)
}
```

## Development

```bash
git clone https://github.com/wpt/ljp
cd ljp
go build ./cmd/ljp/
go test ./...
```

Tests use `httptest.Server` and do not hit the network.

## License

[MIT](./LICENSE)
