package lj

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// FindFirstPostID returns the ID of the oldest post in the journal.
// Uses exponential search + binary search on post URLs.
func FindFirstPostID(ctx context.Context, client *Client, user string) (int, error) {
	lo, hi := 0, 0
	for id := 1; id <= 1000000; id *= 2 {
		exists, err := client.Exists(ctx, client.postURL(user, id))
		if err != nil {
			return 0, err
		}
		client.log("  probing %d... %v\n", id, exists)
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
		client.log("  binary %d... %v\n", mid, exists)
		if exists {
			hi = mid
		} else {
			lo = mid
		}
	}

	return hi, nil
}

// FindLastPostID returns the ID of the newest post in the journal.
func FindLastPostID(ctx context.Context, client *Client, user string) (int, error) {
	resp, err := client.Get(ctx, client.journalURL(user, 0))
	if err != nil {
		return 0, fmt.Errorf("fetch index: %w", err)
	}
	ids, err := ParseJournalIndex(resp.Body)
	resp.Body.Close()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("no posts found for %s", user)
	}
	return ids[0], nil
}

// FetchPostIndex returns all post LJ IDs in chronological order (oldest first).
func FetchPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	seen := make(map[int]bool)
	var all []int
	for skip := 0; ; skip += 20 {
		client.log("Indexing (skip=%d)...\n", skip)

		resp, err := client.Get(ctx, client.journalURL(user, skip))
		if err != nil {
			return nil, fmt.Errorf("fetch index skip=%d: %w", skip, err)
		}

		ids, err := ParseJournalIndex(resp.Body)
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

	// Reverse: LJ returns newest first, we want oldest first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}

	client.log("Indexed %d posts\n", len(all))
	return all, nil
}

// FetchFullPostIndex returns all post IDs by iterating monthly archive pages.
// This catches old posts that FetchPostIndex misses due to LJ index page limits.
func FetchFullPostIndex(ctx context.Context, client *Client, user string) ([]int, error) {
	// First get posts from standard index
	seen := make(map[int]bool)
	var all []int

	// Iterate year/month combinations from 1999 (LJ launch) to current year
	for year := 1999; year <= time.Now().Year(); year++ {
		for month := 1; month <= 12; month++ {
			url := fmt.Sprintf(client.baseURL+"/%d/%02d/", user, year, month)
			client.log("Indexing %d/%02d...\n", year, month)

			resp, err := client.Get(ctx, url)
			if err != nil {
				continue // skip months that error
			}

			ids, err := ParseJournalIndex(resp.Body)
			resp.Body.Close()
			if err != nil {
				continue
			}

			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					all = append(all, id)
				}
			}
		}
	}

	// Sort by ID (chronological)
	sort.Ints(all)

	client.log("Full index: %d posts\n", len(all))
	return all, nil
}

// ParseJournal fetches all posts from a journal, calling onPost for each.
func ParseJournal(ctx context.Context, client *Client, user string, comments bool, onPost func(*Post) error) error {
	seen := make(map[int]bool)
	for skip := 0; ; skip += 20 {
		client.log("Fetching index (skip=%d)...\n", skip)

		resp, err := client.Get(ctx, client.journalURL(user, skip))
		if err != nil {
			return fmt.Errorf("fetch index skip=%d: %w", skip, err)
		}

		ids, err := ParseJournalIndex(resp.Body)
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

			if client.SkipIDs != nil && client.SkipIDs[id] {
				client.log("Skipping post %d (already exists)\n", id)
				continue
			}

			client.log("Fetching post %s/%d...\n", user, id)
			post, err := ParsePost(ctx, client, user, id)
			if err != nil {
				client.log("Warning: skip post %d: %v\n", id, err)
				continue
			}

			if comments {
				post.Comments, err = ParseComments(ctx, client, user, id)
				if err != nil {
					client.log("Warning: comments for %d: %v\n", id, err)
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

// ParseProfileStats fetches the profile page and returns journal entry count.
func ParseProfileStats(ctx context.Context, client *Client, user string) (int, error) {
	resp, err := client.Get(ctx, client.profileURL(user))
	if err != nil {
		return 0, fmt.Errorf("fetch profile: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("parse profile: %w", err)
	}

	val := doc.Find(".b-profile-stat-entrycount .b-profile-stat-value").First()
	if val.Length() == 0 {
		return 0, fmt.Errorf("journal entry count not found on profile page")
	}
	text := strings.ReplaceAll(strings.TrimSpace(val.Text()), ",", "")
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse entry count %q: %w", val.Text(), err)
	}
	return n, nil
}
