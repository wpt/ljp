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
}

type selectorKind int

const (
	selOrdinalList selectorKind = iota
	selOrdinalRange
	selLJIDList
	selLJIDRange
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

	if strings.Contains(arg, "-") {
		parts := strings.SplitN(arg, "-", 2)
		from, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid ordinal range start: %s", parts[0])
		}
		to, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid ordinal range end: %s", parts[1])
		}
		return &selector{kind: selOrdinalRange, ordinals: []int{from, to}}, nil
	}

	parts := strings.Split(arg, ",")
	var ordinals []int
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("invalid ordinal: %s", p)
		}
		ordinals = append(ordinals, n)
	}
	return &selector{kind: selOrdinalList, ordinals: ordinals}, nil
}

func parseLJID(s string) (int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "@")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid LJ ID: @%s", s)
	}
	return n, nil
}

func resolveLJIDs(ctx context.Context, client *lj.Client, user string, sel *selector) ([]int, error) {
	switch sel.kind {
	case selLJIDList:
		return sel.ljIDs, nil
	case selLJIDRange:
		fmt.Fprintf(os.Stderr, "Building post index for LJ ID range...\n")
		index, err := lj.FetchPostIndex(ctx, client, user)
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
			from, to := sel.ordinals[0], sel.ordinals[1]
			if from < 1 {
				from = 1
			}
			if to > len(index) {
				fmt.Fprintf(os.Stderr, "Warning: journal has %d posts, capping range to %d\n", len(index), len(index))
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
