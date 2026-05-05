package lj

import (
	"context"
	"fmt"
	"slices"
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

// FetchPostIndex returns all post LJ IDs in chronological order (oldest first).
func FetchPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	log := client.log()
	host := client.journalHost(user)
	seen := make(map[int]bool)
	var all []int
	for skip := 0; ; skip += 20 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log.Debug("indexing", "skip", skip)

		resp, err := client.Get(ctx, client.journalURL(user, skip))
		if err != nil {
			return nil, fmt.Errorf("fetch index skip=%d: %w", skip, err)
		}

		ids, err := ParseJournalIndex(resp.Body, host)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("parse index skip=%d: %w", skip, err)
		}

		if len(ids) == 0 {
			break
		}

		newPosts := 0
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				all = append(all, id)
				newPosts++
			}
		}

		if newPosts == 0 {
			break
		}
	}

	// Sort ascending = oldest first (LJ IDs grow with creation). More robust than
	// reversing document order, which a pinned/sticky entry would misplace.
	slices.Sort(all)

	log.Info("indexed posts", "count", len(all))
	return all, nil
}

// FetchFullPostIndex returns all post IDs by iterating monthly archive pages.
// This catches old posts that FetchPostIndex misses due to LJ index page limits.
// Fetches months concurrently (capped at HTTPConcurrency). Per-month errors are
// logged as warnings and skipped — a single bad month does not fail the whole index.
// Honour ctx cancellation: cancellation aborts in-flight fetches and returns ctx.Err().
func FetchFullPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log := client.log()
	host := client.journalHost(user)

	// Build the list of (year, month) tuples first, then fan out. The current
	// year is walked in full (not capped at the current month) so future-dated
	// pinned posts are also caught; empty/future months simply return nothing.
	type ym struct{ year, month int }
	now := time.Now()
	var months []ym
	for year := 1999; year <= now.Year(); year++ {
		for month := 1; month <= 12; month++ {
			months = append(months, ym{year, month})
		}
	}

	var mu sync.Mutex
	seen := make(map[int]bool)
	var all []int
	okCount := 0 // months that fetched+parsed OK (guarded by mu)

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
				log.Warn("month fetch failed", "year", m.year, "month", m.month, "err", err)
				return nil
			}

			ids, perr := ParseJournalIndex(resp.Body, host)
			resp.Body.Close()
			if perr != nil {
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

	// Every month failing (not just being empty) means the journal is
	// unreachable — a typo'd username, network outage, or a removed journal.
	// Surface that instead of returning a silent empty "success".
	if okCount == 0 && len(months) > 0 {
		return nil, fmt.Errorf("no archive pages fetched for %s (wrong username or all months failed)", user)
	}

	// Sort by ID (chronological). Required because parallel fetches arrive in
	// non-deterministic order.
	slices.Sort(all)

	log.Info("full index complete", "count", len(all))
	return all, nil
}

// ParseJournal walks the ?skip= index pages sequentially, calling onPost for
// each post (oldest IDs are paginated last). Two caveats: it's the simple
// sequential walker (the CLI uses FetchFullPostIndex + concurrent ParsePost for
// ~10x speedup), and the ?skip= index is capped by LJ, so very old posts can be
// missed — use FetchFullPostIndex for an exhaustive monthly-archive walk.
func ParseJournal(ctx context.Context, client *Client, user string, comments bool, onPost func(*Post) error) error {
	log := client.log()
	host := client.journalHost(user)
	seen := make(map[int]bool)
	for skip := 0; ; skip += 20 {
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

		if len(ids) == 0 {
			break
		}

		newPosts := 0
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			newPosts++

			if client.SkipIDs[id] {
				log.Debug("skipping existing post", "id", id)
				continue
			}

			log.Debug("fetching post", "user", user, "id", id)
			post, err := ParsePost(ctx, client, user, id)
			if err != nil {
				log.Warn("post fetch failed", "id", id, "err", err)
				continue
			}

			if comments {
				post.Comments, err = ParseComments(ctx, client, user, id)
				if err != nil {
					log.Warn("comments fetch failed", "id", id, "err", err)
				}
			}

			if err := onPost(post); err != nil {
				return err
			}
		}

		if newPosts == 0 {
			break
		}
	}
	return nil
}
