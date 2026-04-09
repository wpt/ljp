package lj

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL = "https://%s.livejournal.com"
	defaultBackoff = 1 * time.Second
	maxRetries     = 3
)

type Client struct {
	http         *http.Client
	limiter      *rate.Limiter
	baseURL      string
	retryBackoff time.Duration
	Log          func(format string, args ...any) // progress logger, nil = silent
	BodyFormat   string                           // "html" (default), "markdown", "text"
	ImagesDir    string                           // download images to this dir, empty = skip
	SkipIDs      map[int]bool                     // skip these post IDs (for resume)
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		limiter:      rate.NewLimiter(rate.Every(500*time.Millisecond), 1),
		baseURL:      defaultBaseURL,
		retryBackoff: defaultBackoff,
	}
}

func (c *Client) log(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBackoff * time.Duration(1<<(attempt-1))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "ljp/1.0 (LiveJournal post parser)")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
		}

		return resp, nil
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxRetries, lastErr)
}

// Exists checks if a URL returns 200 without reading the body.
func (c *Client) Exists(ctx context.Context, url string) (bool, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "ljp/1.0 (LiveJournal post parser)")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// DownloadFile downloads a URL to a local file. Skips if file already exists.
func (c *Client) DownloadFile(ctx context.Context, url, destPath string) error {
	if _, err := os.Stat(destPath); err == nil {
		return nil // already exists
	}
	resp, err := c.Get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
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

func (c *Client) profileURL(user string) string {
	return fmt.Sprintf(c.baseURL+"/profile", user)
}
