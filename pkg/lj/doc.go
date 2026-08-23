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
// image fetches. The default 30 sits under LiveJournal's throttling threshold
// for sustained runs. Call [Client.SetConcurrency] to retune both in lockstep — direct
// field assignment changes only the errgroup width and leaves the Transport
// pool stale.
//
// # Image download
//
// Set [Client.ImagesDir] to a writable directory and ParsePost will fetch
// every http(s) <img src> in the post body — and ParseComments in every
// comment body — to {dir}/{sha256_16hex}.{ext}, rewriting src to the local
// path. Skipped for [FormatText], where the rewritten src would be discarded.
// data:/javascript:/vbscript: URIs and non-http(s) URLs are skipped.
//
// [Client.ImagesDir] says where bytes are written; [Client.ImagesRef] says what
// prefix goes into the rewritten src, and defaults to ImagesDir. Set ImagesRef
// whenever you save the body somewhere other than the working directory — a
// browser resolves a relative src against the document's own URL, so a body
// written to posts/123.html with images in img/ needs ImagesRef "../img".
//
// # Body format
//
// [Client.BodyFormat] controls the Body field on [Post]: one of [FormatHTML]
// (default), [FormatMarkdown] (own goquery-based converter), or [FormatText]
// (stripped to plain text).
//
// # Indexing strategies
//
// Three index walkers. [FetchPostIndex] paginates ?skip=N — fast, but LJ caps
// these and very old posts get truncated. [FetchFullPostIndex] walks /YYYY/MM/
// monthly archives 1999..now — slower, exhaustive, parallel, tolerant of
// individual failed months. [FetchMonthlyPostIndex] walks an explicit month
// range and errors if any requested month fails to fetch.
package lj
