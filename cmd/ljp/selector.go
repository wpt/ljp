package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wpt/ljp/pkg/lj"
)

type selector struct {
	kind     selectorKind
	ordinals []int
	ljIDs    []int
	dates    []int // fromYear, fromMonth, toYear, toMonth (selDateRange only)
}

type selectorKind int

const (
	selOrdinalList selectorKind = iota
	selOrdinalRange
	selLJIDList
	selLJIDRange
	selDateRange
)

func parseSelector(arg string) (*selector, error) {
	if strings.HasPrefix(arg, "@") {
		if strings.Contains(arg, "-") {
			parts := strings.SplitN(arg, "-", 2)
			from, err := parseLJID(parts[0])
			if err != nil {
				return nil, err
			}
			to, err := parseLJID(parts[1])
			if err != nil {
				return nil, err
			}
			if from > to {
				return nil, fmt.Errorf("inverted LJ ID range: @%d-@%d", from, to)
			}
			return &selector{kind: selLJIDRange, ljIDs: []int{from, to}}, nil
		}
		parts := strings.Split(arg, ",")
		var ids []int
		for _, p := range parts {
			id, err := parseLJID(p)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return &selector{kind: selLJIDList, ljIDs: ids}, nil
	}

	// A slash routes to the date form (YYYY/MM); plain ordinals and @LJ IDs
	// never contain one, so the grammar stays unambiguous.
	if strings.Contains(arg, "/") {
		return parseDateSelector(arg)
	}

	if strings.Contains(arg, "-") {
		parts := strings.SplitN(arg, "-", 2)
		from, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || from < 1 {
			return nil, fmt.Errorf("invalid ordinal range start (ordinals are 1-based): %s", parts[0])
		}
		to, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid ordinal range end: %s", parts[1])
		}
		if from > to {
			return nil, fmt.Errorf("inverted ordinal range: %d-%d", from, to)
		}
		return &selector{kind: selOrdinalRange, ordinals: []int{from, to}}, nil
	}

	parts := strings.Split(arg, ",")
	var ordinals []int
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		// Reject non-positive ordinals at parse time, like @0 for LJ IDs —
		// they'd otherwise burn a full index fetch just to warn and exit 0.
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid ordinal (ordinals are 1-based): %s", p)
		}
		ordinals = append(ordinals, n)
	}
	return &selector{kind: selOrdinalList, ordinals: ordinals}, nil
}

// parseDateSelector parses "YYYY/MM" (one month) or "YYYY/MM-YYYY/MM" (an
// inclusive month range) into a selDateRange selector.
func parseDateSelector(arg string) (*selector, error) {
	fromStr, toStr := arg, arg
	if i := strings.Index(arg, "-"); i >= 0 {
		fromStr, toStr = arg[:i], arg[i+1:]
	}
	fromYear, fromMonth, err := parseYearMonth(fromStr)
	if err != nil {
		return nil, err
	}
	toYear, toMonth, err := parseYearMonth(toStr)
	if err != nil {
		return nil, err
	}
	if fromYear*100+fromMonth > toYear*100+toMonth {
		return nil, fmt.Errorf("inverted date range: %s", arg)
	}
	return &selector{kind: selDateRange, dates: []int{fromYear, fromMonth, toYear, toMonth}}, nil
}

// parseYearMonth parses one "YYYY/MM" bound. Years before LiveJournal existed
// (1999) are rejected as likely typos.
func parseYearMonth(s string) (year, month int, err error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid date selector %q (want YYYY/MM)", s)
	}
	year, err = strconv.Atoi(parts[0])
	if err != nil || year < 1999 || year > 2099 {
		return 0, 0, fmt.Errorf("invalid year in date selector %q (want 1999-2099)", s)
	}
	month, err = strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return 0, 0, fmt.Errorf("invalid month in date selector %q (want 01-12)", s)
	}
	return year, month, nil
}

func parseLJID(s string) (int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "@")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid LJ ID: @%s", s)
	}
	return n, nil
}

func resolveLJIDs(ctx context.Context, client *lj.Client, user string, sel *selector) ([]int, error) {
	switch sel.kind {
	case selLJIDList:
		return sel.ljIDs, nil
	case selDateRange:
		d := sel.dates
		fmt.Fprintf(os.Stderr, "Building monthly post index %04d/%02d-%04d/%02d...\n", d[0], d[1], d[2], d[3])
		return lj.FetchMonthlyPostIndex(ctx, client, user, d[0], d[1], d[2], d[3])
	case selLJIDRange:
		fmt.Fprintf(os.Stderr, "Building full post index for LJ ID range...\n")
		index, err := lj.FetchFullPostIndex(ctx, client, user)
		if err != nil {
			return nil, err
		}
		from, to := sel.ljIDs[0], sel.ljIDs[1]
		var ids []int
		for _, id := range index {
			if id >= from && id <= to {
				ids = append(ids, id)
			}
		}
		return ids, nil
	case selOrdinalList, selOrdinalRange:
		fmt.Fprintf(os.Stderr, "Building post index...\n")
		index, err := lj.FetchPostIndex(ctx, client, user)
		if err != nil {
			return nil, err
		}
		if sel.kind == selOrdinalRange {
			from, to := sel.ordinals[0], sel.ordinals[1] // from >= 1, enforced at parse time
			if from > len(index) {
				return nil, fmt.Errorf("ordinal #%d out of range (journal has %d indexable posts)", from, len(index))
			}
			if to > len(index) {
				fmt.Fprintf(os.Stderr, "Warning: requested range end %d exceeds journal size %d, capping to %d\n", to, len(index), len(index))
				to = len(index)
			}
			return index[from-1 : to], nil
		}
		var ids []int
		for _, n := range sel.ordinals {
			if n < 1 || n > len(index) {
				fmt.Fprintf(os.Stderr, "Warning: ordinal #%d out of range (journal has %d posts)\n", n, len(index))
				continue
			}
			ids = append(ids, index[n-1])
		}
		return ids, nil
	}
	return nil, fmt.Errorf("unknown selector kind")
}
