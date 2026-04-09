# ljp

[![CI](https://github.com/wpt/ljp/actions/workflows/ci.yml/badge.svg)](https://github.com/wpt/ljp/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/wpt/ljp)](https://goreportcard.com/report/github.com/wpt/ljp)

CLI tool and Go library for fetching public LiveJournal posts and comments. No authentication required.

## Install

```bash
go install github.com/wpt/ljp/cmd/ljp@latest
```

## Usage

```bash
# Single post (by ID or URL)
ljp news/166511
ljp https://news.livejournal.com/166511.html

# With comments
ljp --comments news/166511

# Save to file
ljp -o post.json news/166511

# Entire journal (JSONL to stdout)
ljp news

# Post selection (ordinal: 1 = oldest)
ljp news 1-10                    # first 10 posts
ljp news 1,5,100                 # specific posts
ljp news @166511                 # by LJ ID
ljp news @256,@166511            # multiple LJ IDs
ljp news @256-@100000            # LJ ID range

# Journal info
ljp --count news                 # entry count
ljp --first news                 # oldest post
ljp --last news                  # newest post

# Bulk download
ljp --comments --dir ./posts news       # one JSON per post
ljp --resume --dir ./posts news         # skip already downloaded

# Format & images
ljp --format markdown news/166511       # body as Markdown
ljp --format text news/166511           # body as plain text
ljp --images ./img news/166511          # download images locally

# View as HTML
ljp --render --comments -o post.html news/166511
```

### Flags

| Flag | Description |
|------|-------------|
| `--comments` | Include comments |
| `--count` | Show journal entry count and exit |
| `--first` | Fetch the oldest post |
| `--last` | Fetch the newest post |
| `--format html\|markdown\|text` | Body format (default: html) |
| `--images <dir>` | Download images to directory |
| `--render` | Output as HTML page instead of JSON |
| `--resume` | Skip already downloaded posts (with `--dir`) |
| `-o <file>` | Output to file |
| `--dir <dir>` | Output directory (one file per post) |

## Output

Single post: pretty JSON. Multiple posts: JSONL (one JSON object per line).

```json
{
  "id": 166511,
  "url": "https://news.livejournal.com/166511.html",
  "title": "Post Title",
  "date": "December 17 2025, 16:01",
  "date_unix": 1734451260,
  "author": "news",
  "body": "<p>Post content...</p>",
  "tags": ["updates"],
  "comments": [
    {
      "id": 10193184,
      "parent_id": 0,
      "author": "user",
      "body": "Comment text...",
      "children": []
    }
  ]
}
```

## Library

```go
import "github.com/wpt/ljp/pkg/lj"

client := lj.NewClient()
ctx := context.Background()

// Single post + comments
post, _ := lj.ParsePost(ctx, client, "news", 166511)
post.Comments, _ = lj.ParseComments(ctx, client, "news", 166511)

// Options
client.BodyFormat = "markdown"   // "html" (default), "markdown", "text"
client.ImagesDir = "./images"    // download images locally

// Journal operations
count, _ := lj.ParseProfileStats(ctx, client, "news")
firstID, _ := lj.FindFirstPostID(ctx, client, "news")
index, _ := lj.FetchPostIndex(ctx, client, "news") // []int, chronological

// Stream all posts with callback
lj.ParseJournal(ctx, client, "news", true, func(p *lj.Post) error {
    // process each post
    return nil
})
```

## License

MIT
