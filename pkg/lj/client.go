package lj

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	defaultBaseURL = "https://%s.livejournal.com"
	defaultBackoff = 1 * time.Second
	// maxBackoff caps the exponential delay. With maxRetries=5 the delays are
	// 1s, 2s, 4s, 8s between attempts, so 8s is the largest delay actually
	// reachable; the cap is a defensive ceiling in case maxRetries is later
	// bumped (1<<5 = 32s otherwise).
	maxBackoff = 8 * time.Second
	maxRetries = 5
	// maxRetryAfter caps how long a server-supplied Retry-After header can push
	// a single backoff. Without a ceiling a hostile or buggy header could stall
	// a fetch for minutes; the caller ctx still bounds total time.
	maxRetryAfter = 60 * time.Second
	userAgent     = "ljp (LiveJournal post parser)"
)

// StatusError is returned when a request reaches a terminal non-OK HTTP status
// (after the retry loop gives up on 5xx/429). It carries the status code and
// URL so callers can errors.As it and distinguish, e.g., an expected 404
// (deleted post during an archive walk) from retry-exhausted throttling.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d for %s", e.Code, e.URL)
}

// tmpCounter makes per-download temp filenames unique so two concurrent
// downloads of the same target (e.g. one image URL referenced by several posts
// fetched in parallel) never share a {name}.tmp and race on create/rename.
var tmpCounter atomic.Uint64

// HTTPConcurrency is the default cap on simultaneous connections to one host,
// also used as the default fan-out width for parallel page/post fetches. 40 is
// the sweet spot from empirical sweep on LJ: at the knee of the latency curve,
// well clear of LJ's throttling threshold. NewClient copies this into
// Client.HTTPConcurrency at construction time; call Client.SetConcurrency to
// change the limit at runtime (direct field assignment leaves the underlying
// Transport pool stale).
var HTTPConcurrency = 40

// BodyFormat values accepted by Client.BodyFormat. Empty string is treated as
// FormatHTML. Unknown values warn-and-fall-through to FormatHTML at parse time.
const (
	FormatHTML     = "html"
	FormatMarkdown = "markdown"
	FormatText     = "text"
)

// discardLogger is shared so Client.log() doesn't allocate on every call when
// Logger is nil (which is the common library-embedded case).
var discardLogger = slog.New(slog.DiscardHandler)

// Client is a LiveJournal HTTP client with retry, fan-out, and optional
// sandboxed image download. Obtain one via NewClient and mutate the exported
// fields before issuing requests — Transport and concurrency knobs are read at
// first request and are not safe to change concurrently with active fetches.
type Client struct {
	http         *http.Client
	baseURL      string
	retryBackoff time.Duration
	Logger       *slog.Logger // progress logger, nil = silent
	BodyFormat   string       // "html" (default), "markdown", "text"
	ImagesDir    string       // download images to this dir, empty = skip
	SkipIDs      map[int]bool // skip these post IDs (for resume)
	// HTTPConcurrency is the fan-out width for parallel post/comment-page/
	// image fetches AND the Transport's MaxConnsPerHost. Use SetConcurrency
	// to mutate this safely — direct assignment changes only the errgroup
	// width and leaves the Transport pool stale.
	HTTPConcurrency int
}

// NewClient returns a Client with default settings: 30s request timeout,
// MaxConnsPerHost = HTTPConcurrency (package var, default 40), exponential
// retry backoff starting at 1s capped at 8s. Set BodyFormat, ImagesDir,
// Logger, SkipIDs on the returned value before the first request. To change
// the concurrency cap after construction, call SetConcurrency — direct
// assignment to HTTPConcurrency desyncs the underlying Transport pool.
func NewClient() *Client {
	// Single shared Transport, keep idle conns warm, allow plenty of parallel
	// in-flight requests. No rate limiter — LJ tolerates the load easily; the real
	// throughput cap is MaxConnsPerHost, kept in sync with Client.HTTPConcurrency.
	n := HTTPConcurrency
	// Clone the default Transport when it's the standard type; fall back to a
	// fresh one if an embedding program replaced http.DefaultTransport with an
	// instrumentation RoundTripper (otelhttp, proxies) so NewClient never panics
	// on an unchecked type assertion.
	var t *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	} else {
		t = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t.MaxConnsPerHost = n
	t.MaxIdleConnsPerHost = n
	// MaxIdleConns is a global pool cap (default 100); keep it >= n so a high
	// --concurrency can actually retain one warm idle conn per worker instead of
	// churning TLS handshakes between fan-out waves.
	t.MaxIdleConns = max(100, n)
	t.IdleConnTimeout = 90 * time.Second
	// Bound only the wait for response *headers*, not the whole request. A whole-
	// request timeout (http.Client.Timeout) also covers body reads, so a large
	// image or a 1MB+ comment JSON on a slow link would die mid-body and never
	// retry; it would also fire while a request waits for a pooled connection.
	// Total time stays bounded by the caller ctx (CLI signal cancellation).
	t.ResponseHeaderTimeout = 30 * time.Second
	return &Client{
		http:            &http.Client{Transport: t},
		baseURL:         defaultBaseURL,
		retryBackoff:    defaultBackoff,
		HTTPConcurrency: n,
	}
}

// log returns the configured logger or a shared discard logger.
func (c *Client) log() *slog.Logger {
	if c.Logger == nil {
		return discardLogger
	}
	return c.Logger
}

// concurrency returns the effective fan-out width, ensuring callers using
// errgroup.SetLimit don't accidentally pass 0 (which means "unlimited").
func (c *Client) concurrency() int {
	if c.HTTPConcurrency <= 0 {
		return 1
	}
	return c.HTTPConcurrency
}

// SetBaseURL overrides the LiveJournal base URL template (default
// "https://%s.livejournal.com"). The string MUST contain exactly one %s for
// the journal username. Useful for tests pointing at httptest.Server or for
// pointing at an LJ-compatible mirror — most callers should leave this alone.
// Call before issuing requests.
func (c *Client) SetBaseURL(format string) {
	c.baseURL = format
}

// SetConcurrency adjusts fan-out width AND the underlying http.Transport's
// per-host connection pool so the two stay in lockstep. Direct assignment to
// c.HTTPConcurrency changes only the errgroup width — the Transport keeps its
// NewClient-time pool size, silently capping --concurrency at whatever the
// package-level HTTPConcurrency was when NewClient ran. Use this instead.
//
// Call before issuing requests; mutating the Transport pool while fetches are
// in flight is not safe.
func (c *Client) SetConcurrency(n int) {
	if n < 1 {
		n = 1
	}
	c.HTTPConcurrency = n
	if t, ok := c.http.Transport.(*http.Transport); ok {
		t.MaxConnsPerHost = n
		t.MaxIdleConnsPerHost = n
		t.MaxIdleConns = max(100, n)
	}
}

// journalHost returns the host (e.g. "news.livejournal.com") this client uses
// for the given journal, derived from the baseURL template. Empty if it can't
// be resolved. ParseJournalIndex uses it to accept this journal's absolute post
// links — the form real LiveJournal emits — while rejecting cross-journal ones.
func (c *Client) journalHost(user string) string {
	u, err := url.Parse(fmt.Sprintf(c.baseURL, user))
	if err != nil {
		return ""
	}
	return u.Host
}

// do runs one HTTP request through the retry loop. Used by both Get and Exists
// so a single transient 5xx/429 on HEAD doesn't abort callers like
// FindFirstPostID's binary search. Retries on transport errors, 5xx, and 429
// with exponential backoff capped at maxBackoff. Honours caller cancellation
// immediately. On any non-OK terminal status the response body is closed and an
// error is returned; callers that succeed own resp.Body.
func (c *Client) do(ctx context.Context, method, url string) (*http.Response, error) {
	if c.http == nil {
		return nil, fmt.Errorf("lj: Client not initialised — use lj.NewClient")
	}
	log := c.log()
	var lastErr error
	var retryAfter time.Duration // honoured on the next attempt, from Retry-After
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBackoff * time.Duration(1<<(attempt-1))
			if delay > maxBackoff {
				delay = maxBackoff
			}
			// A server-supplied Retry-After (429/503) wins if it's longer, so we
			// don't hammer a throttling server on the fixed ladder and fail fast.
			if retryAfter > delay {
				delay = retryAfter
			}
			log.Debug("retrying", "url", url, "attempt", attempt+1, "delay", delay, "prev_err", lastErr)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			// Retry on transport errors (timeouts, connection resets, DNS hiccups).
			// Honour explicit caller cancellation though: if ctx itself is dead, bail.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			// Drain a bounded amount before closing so the keep-alive connection
			// returns to the pool instead of being discarded (error pages are tiny).
			drainAndClose(resp.Body)
			lastErr = &StatusError{Code: resp.StatusCode, URL: url}
			continue
		}

		return resp, nil
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxRetries, lastErr)
}

// drainAndClose reads up to a few KB of an unwanted response body before closing
// so net/http can reuse the connection (closing with unread data discards it).
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	body.Close()
}

// parseRetryAfter parses the integer-seconds form of a Retry-After header,
// clamped to maxRetryAfter. Returns 0 for missing/HTTP-date/invalid values.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

// Get performs a GET through the shared retry loop and returns the response
// on HTTP 200. Any non-200 terminal status (after 5xx/429 retries) is returned
// as an error with resp.Body already closed; on success the caller owns the
// body. Honours ctx cancellation at every retry step. Sets the package
// User-Agent automatically.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodGet, url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		drainAndClose(resp.Body)
		return nil, &StatusError{Code: resp.StatusCode, URL: url}
	}
	return resp, nil
}

// Exists checks if a URL returns 200 without reading the body. Goes through the
// same retry loop as Get so a transient 5xx/429 on HEAD doesn't poison
// FindFirstPostID's binary search. (false, nil) means a terminal non-200
// (typically 404); (false, err) means transport failure that exhausted retries.
func (c *Client) Exists(ctx context.Context, url string) (bool, error) {
	resp, err := c.do(ctx, http.MethodHead, url)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// downloadCore is the shared implementation of downloadFile and downloadInto.
// `existing` reports current file size and whether it exists (size 0 + exists
// counts as incomplete and is retried — a 0-byte leftover from a crashed run
// must not be treated as "done"). `create` opens the temp file for writing.
// `finalize` atomically promotes the temp file to its final name. Splitting
// these three closures lets us share the body for both raw-filesystem and
// *os.Root code paths without duplicating the GET + io.Copy + rename block.
//
// Returns the response Content-Type alongside the error so callers downloading
// extension-less URLs can correct the filename via the server's MIME hint.
// An empty string is returned when the file was skipped (already present) or
// the response carried no Content-Type header.
func (c *Client) downloadCore(
	ctx context.Context,
	url string,
	existing func() (size int64, ok bool),
	create func() (io.WriteCloser, error),
	finalize func() error,
	cleanup func(),
) (string, error) {
	if size, ok := existing(); ok && size > 0 {
		return "", nil
	}
	resp, err := c.Get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")

	f, err := create()
	if err != nil {
		return contentType, err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		cleanup()
		return contentType, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return contentType, err
	}
	if err := finalize(); err != nil {
		cleanup()
		return contentType, err
	}
	return contentType, nil
}

// downloadFile downloads a URL to a local file. Skips if a non-empty file
// already exists; writes through a {dest}.tmp + rename so a crashed/cancelled
// run leaves no half-written file at destPath. Unexported because the
// production image pipeline uses downloadInto (sandboxed via *os.Root); this
// helper exists for tests covering the shared downloadCore primitive.
func (c *Client) downloadFile(ctx context.Context, url, destPath string) error {
	tmp := fmt.Sprintf("%s.%d.tmp", destPath, tmpCounter.Add(1))
	_, err := c.downloadCore(
		ctx, url,
		func() (int64, bool) {
			st, err := os.Stat(destPath)
			if err != nil {
				return 0, false
			}
			return st.Size(), true
		},
		func() (io.WriteCloser, error) { return os.Create(tmp) },
		func() error { return os.Rename(tmp, destPath) },
		func() { os.Remove(tmp) },
	)
	return err
}

// downloadInto downloads URL into root/name with the same skip-non-empty +
// tmp+rename semantics as downloadFile. Using *os.Root keeps writes sandboxed
// inside the root — name cannot escape via "..", absolute paths, or symlinks
// pointing outside. Returns the response Content-Type so callers can fix
// extension-less filenames.
func (c *Client) downloadInto(ctx context.Context, root *os.Root, url, name string) (string, error) {
	tmp := fmt.Sprintf("%s.%d.tmp", name, tmpCounter.Add(1))
	return c.downloadCore(
		ctx, url,
		func() (int64, bool) {
			st, err := root.Stat(name)
			if err != nil {
				return 0, false
			}
			return st.Size(), true
		},
		func() (io.WriteCloser, error) { return root.Create(tmp) },
		func() error { return root.Rename(tmp, name) },
		func() { root.Remove(tmp) },
	)
}

func (c *Client) postURL(user string, id int) string {
	return fmt.Sprintf(c.baseURL+"/%d.html", user, id)
}

func (c *Client) commentsURL(user string, id, page int) string {
	return fmt.Sprintf(c.baseURL+"/%d.html?view=flat&page=%d&format=light", user, id, page)
}

func (c *Client) journalURL(user string, skip int) string {
	return fmt.Sprintf(c.baseURL+"/?skip=%d", user, skip)
}

func (c *Client) monthURL(user string, year, month int) string {
	return fmt.Sprintf(c.baseURL+"/%d/%02d/", user, year, month)
}
