package lj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// maxFirstPostProbe caps the exponential probe in FindFirstPostID. The loop
// doubles id and probes while id <= this value, so the highest ID actually
// probed is this exact power of two (2^20 = 1048576) — comfortably above any
// real LJ first-post ditemid.
const maxFirstPostProbe = 1 << 20

// FindFirstPostID returns the ID of the oldest post in the journal, using an
// exponential probe + binary search over HEAD requests. It assumes existence is
// roughly monotonic near the start of the journal (the earliest IDs are dense);
// if the very first posts were deleted it may return a slightly later surviving
// post rather than the true earliest, since a binary search can't see gaps.
func FindFirstPostID(ctx context.Context, client *Client, user string) (int, error) {
	log := client.log()
	lo, hi := 0, 0
	for id := 1; id <= maxFirstPostProbe; id *= 2 {
		exists, err := client.Exists(ctx, client.postURL(user, id))
		if err != nil {
			return 0, err
		}
		log.Debug("probe", "id", id, "exists", exists)
		if exists {
			hi = id
			lo = id / 2
			break
		}
	}
	if hi == 0 {
		return 0, fmt.Errorf("no posts found for %s", user)
	}

	for lo+1 < hi {
		mid := (lo + hi) / 2
		exists, err := client.Exists(ctx, client.postURL(user, mid))
		if err != nil {
			return 0, err
		}
		log.Debug("binary search", "mid", mid, "exists", exists)
		if exists {
			hi = mid
		} else {
			lo = mid
		}
	}

	return hi, nil
}

// FindLastPostID returns the ID of the newest post in the journal. It takes the
// highest post ID on the first index page rather than the topmost link, so a
// pinned/sticky entry (which LJ floats to the top regardless of age) doesn't
// masquerade as the newest post — LJ post IDs increase with creation.
func FindLastPostID(ctx context.Context, client *Client, user string) (int, error) {
	resp, err := client.Get(ctx, client.journalURL(user, 0))
	if err != nil {
		return 0, fmt.Errorf("fetch index: %w", err)
	}
	ids, err := ParseJournalIndex(resp.Body, client.journalHost(user))
	resp.Body.Close()
	if err != nil {
		return 0, fmt.Errorf("parse index: %w", err)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("no posts found for %s", user)
	}
	return slices.Max(ids), nil
}

// forEachNewIndexID walks the ?skip= index pages sequentially, calling fn once
// per previously-unseen post ID in document order. Stops when a page yields no
// new IDs — LJ returns the last page's content forever for out-of-range skips.
// An error from fn aborts the walk and is returned as-is.
func forEachNewIndexID(ctx context.Context, client *Client, user string, fn func(id int) error) error {
	log := client.log()
	host := client.journalHost(user)
	seen := make(map[int]bool)
	for skip := 0; ; skip += 20 {
		if err := ctx.Err(); err != nil {
			return err
		}
		log.Debug("fetching index", "skip", skip)

		resp, err := client.Get(ctx, client.journalURL(user, skip))
		if err != nil {
			return fmt.Errorf("fetch index skip=%d: %w", skip, err)
		}

		ids, err := ParseJournalIndex(resp.Body, host)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("parse index skip=%d: %w", skip, err)
		}

		newPosts := 0
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			newPosts++
			if err := fn(id); err != nil {
				return err
			}
		}

		if newPosts == 0 {
			return nil
		}
	}
}

// FetchPostIndex returns all post LJ IDs in chronological order (oldest first).
func FetchPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	var all []int
	err := forEachNewIndexID(ctx, client, user, func(id int) error {
		all = append(all, id)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort ascending = oldest first (LJ IDs grow with creation). More robust than
	// reversing document order, which a pinned/sticky entry would misplace.
	slices.Sort(all)

	client.log().Info("indexed posts", "count", len(all))
	return all, nil
}

// FetchFullPostIndex returns all post IDs by iterating monthly archive pages.
// This catches old posts that FetchPostIndex misses due to LJ index page limits.
// The current year is walked in full (not capped at the current month) so
// future-dated pinned posts are also caught; empty/future months simply return
// nothing. Unlike FetchMonthlyPostIndex, a failed month here is a logged
// warning, not an error — losing one month of 300+ to a transient failure
// shouldn't abort a whole-journal walk.
func FetchFullPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	return fetchMonthlyIndex(ctx, client, user, 1999, 1, time.Now().Year(), 12, false)
}

// FetchMonthlyPostIndex returns the post IDs found on the /YYYY/MM/ monthly
// archive pages from fromYear/fromMonth to toYear/toMonth inclusive, sorted
// ascending (chronological). Fetches months concurrently (capped at
// HTTPConcurrency). A month whose archive page is missing (404) simply
// contributes no posts, but any month that fails to fetch or parse (5xx/429
// after retries, transport errors) makes the whole call return an error —
// for a user-specified range, silently dropping a failed month would present
// an incomplete result as a complete one. An invalid range is an error.
// Honours ctx cancellation: it aborts in-flight fetches and returns ctx.Err().
func FetchMonthlyPostIndex(ctx context.Context, client *Client, user string, fromYear, fromMonth, toYear, toMonth int) ([]int, error) {
	return fetchMonthlyIndex(ctx, client, user, fromYear, fromMonth, toYear, toMonth, true)
}

// fetchMonthlyIndex implements the monthly-archive walk. strict controls the
// failed-month policy: error out (user-specified ranges) vs warn-and-skip
// (the exhaustive 300+-month whole-journal walk).
func fetchMonthlyIndex(ctx context.Context, client *Client, user string, fromYear, fromMonth, toYear, toMonth int, strict bool) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fromMonth < 1 || fromMonth > 12 || toMonth < 1 || toMonth > 12 {
		return nil, fmt.Errorf("month out of range in %04d/%02d-%04d/%02d", fromYear, fromMonth, toYear, toMonth)
	}
	if fromYear*100+fromMonth > toYear*100+toMonth {
		return nil, fmt.Errorf("inverted month range: %04d/%02d-%04d/%02d", fromYear, fromMonth, toYear, toMonth)
	}
	log := client.log()
	host := client.journalHost(user)

	// Build the list of (year, month) tuples first, then fan out.
	type ym struct{ year, month int }
	var months []ym
	for year := fromYear; year <= toYear; year++ {
		m1, m2 := 1, 12
		if year == fromYear {
			m1 = fromMonth
		}
		if year == toYear {
			m2 = toMonth
		}
		for month := m1; month <= m2; month++ {
			months = append(months, ym{year, month})
		}
	}

	var mu sync.Mutex
	seen := make(map[int]bool)
	var all []int
	okCount := 0              // months that fetched+parsed OK (guarded by mu)
	var failedMonths []string // months that failed with a non-404 error (guarded by mu)
	var firstErr error        // first failed month's error (guarded by mu)

	recordFailure := func(year, month int, err error) {
		mu.Lock()
		failedMonths = append(failedMonths, fmt.Sprintf("%04d/%02d", year, month))
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	eg, ectx := errgroup.WithContext(ctx)
	eg.SetLimit(client.concurrency())
	for _, m := range months {
		if ectx.Err() != nil {
			break
		}
		eg.Go(func() error {
			if ectx.Err() != nil {
				return ectx.Err()
			}
			url := client.monthURL(user, m.year, m.month)
			log.Debug("indexing month", "year", m.year, "month", m.month)

			resp, err := client.Get(ectx, url)
			if err != nil {
				if ectx.Err() != nil {
					return ectx.Err()
				}
				// A 404 month is not a failure: LJ has no archive page before
				// the journal's first post (and none for empty months) — it
				// simply contributes no posts. Anything else (5xx/429 after
				// retries, transport errors) is a real failure.
				var se *StatusError
				if !(errors.As(err, &se) && se.Code == http.StatusNotFound) {
					recordFailure(m.year, m.month, err)
				}
				log.Warn("month fetch failed", "year", m.year, "month", m.month, "err", err)
				return nil
			}

			ids, perr := ParseJournalIndex(resp.Body, host)
			resp.Body.Close()
			if perr != nil {
				recordFailure(m.year, m.month, perr)
				log.Warn("month parse failed", "year", m.year, "month", m.month, "err", perr)
				return nil
			}

			mu.Lock()
			okCount++
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					all = append(all, id)
				}
			}
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// In strict mode any failed month poisons the result: a user-specified
	// range with a silently-missing month would look complete while it isn't.
	if strict && len(failedMonths) > 0 {
		slices.Sort(failedMonths) // parallel completion order is random
		return nil, fmt.Errorf("%d archive month(s) failed (%s): %w",
			len(failedMonths), strings.Join(failedMonths, ", "), firstErr)
	}

	// Every month failing or missing (not just being empty) means the journal
	// is unreachable — a typo'd username, network outage, or a removed
	// journal. Surface that instead of returning a silent empty "success".
	if okCount == 0 && len(months) > 0 {
		return nil, fmt.Errorf("no archive pages fetched for %s (wrong username, or none of the %d requested months exist)", user, len(months))
	}

	// Sort by ID (chronological). Required because parallel fetches arrive in
	// non-deterministic order.
	slices.Sort(all)

	log.Info("monthly index complete", "count", len(all))
	return all, nil
}

// ParseJournal walks the ?skip= index pages sequentially, calling onPost for
// each post (oldest IDs are paginated last). Two caveats: it's the simple
// sequential walker (the CLI uses FetchFullPostIndex + concurrent ParsePost for
// ~10x speedup), and the ?skip= index is capped by LJ, so very old posts can be
// missed — use FetchFullPostIndex for an exhaustive monthly-archive walk.
func ParseJournal(ctx context.Context, client *Client, user string, comments bool, onPost func(*Post) error) error {
	log := client.log()
	return forEachNewIndexID(ctx, client, user, func(id int) error {
		if client.SkipIDs[id] {
			log.Debug("skipping existing post", "id", id)
			return nil
		}

		log.Debug("fetching post", "user", user, "id", id)
		post, err := ParsePost(ctx, client, user, id)
		if err != nil {
			log.Warn("post fetch failed", "id", id, "err", err)
			return nil
		}

		if comments {
			post.Comments, err = ParseComments(ctx, client, user, id)
			if err != nil {
				log.Warn("comments fetch failed", "id", id, "err", err)
			}
		}

		return onPost(post)
	})
}
