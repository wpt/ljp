// Package lj parses and downloads public LiveJournal posts and comments.
//
// No authentication required — only the public flat-view HTML is consumed.
// The package is split into small composable pieces; a typical caller wires
// them with a [Client]:
//
//	client := lj.NewClient()
//	post, err := lj.ParsePost(ctx, client, "news", 166511)
//	if err != nil { ... }
//	post.Comments, err = lj.ParseComments(ctx, client, "news", 166511)
//
// # Concurrency
//
// One knob, [Client.HTTPConcurrency], drives the Transport's per-host
// connection pool AND the errgroup fan-out for parallel post/comment-page/
// image fetches. The default 40 is calibrated against LiveJournal's tolerance
// curve. Call [Client.SetConcurrency] to retune both in lockstep — direct
// field assignment changes only the errgroup width and leaves the Transport
// pool stale.
//
// # Image download
//
// Set [Client.ImagesDir] to a writable directory and ParsePost will fetch
// every http(s) <img src> in the post body to {dir}/{sha256_16hex}.{ext},
// rewriting src to the local path. data:/javascript:/vbscript: URIs and
// non-http(s) URLs are skipped.
//
// # Body format
//
// [Client.BodyFormat] controls the Body field on [Post]: one of [FormatHTML]
// (default), [FormatMarkdown] (own goquery-based converter), or [FormatText]
// (stripped to plain text).
//
// # Indexing strategies
//
// Two complementary index walkers. [FetchPostIndex] paginates ?skip=N — fast,
// but LJ caps these and very old posts get truncated. [FetchFullPostIndex]
// walks /YYYY/MM/ monthly archives 1999..now — slower, exhaustive, parallel.
package lj
