package lj

import (
	"testing"
	"time"
)

func TestBuildCommentTree(t *testing.T) {
	t.Run("flat to tree", func(t *testing.T) {
		flat := []*Comment{
			{ID: 1, ParentID: 0, Body: "root1"},
			{ID: 2, ParentID: 1, Body: "child of root1"},
			{ID: 3, ParentID: 0, Body: "root2"},
			{ID: 4, ParentID: 2, Body: "grandchild"},
		}
		roots := BuildCommentTree(flat)
		if len(roots) != 2 {
			t.Fatalf("roots = %d, want 2", len(roots))
		}
		if len(roots[0].Children) != 1 {
			t.Fatalf("root1 children = %d, want 1", len(roots[0].Children))
		}
		if roots[0].Children[0].Body != "child of root1" {
			t.Errorf("child body = %q", roots[0].Children[0].Body)
		}
		if len(roots[0].Children[0].Children) != 1 {
			t.Fatalf("grandchildren = %d, want 1", len(roots[0].Children[0].Children))
		}
		if roots[0].Children[0].Children[0].Body != "grandchild" {
			t.Errorf("grandchild body = %q", roots[0].Children[0].Children[0].Body)
		}
	})

	t.Run("orphan becomes root", func(t *testing.T) {
		flat := []*Comment{
			{ID: 1, ParentID: 999, Body: "orphan"},
		}
		roots := BuildCommentTree(flat)
		if len(roots) != 1 {
			t.Fatalf("roots = %d, want 1", len(roots))
		}
		if roots[0].Body != "orphan" {
			t.Errorf("body = %q", roots[0].Body)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		roots := BuildCommentTree(nil)
		if roots != nil {
			t.Errorf("expected nil, got %d roots", len(roots))
		}
	})

	t.Run("idempotent on duplicate input", func(t *testing.T) {
		// Simulates LJ's 'last page returns forever' over-fetch: duplicate
		// comment IDs in the flat list must not produce duplicate children.
		root := &Comment{ID: 1, ParentID: 0, Body: "root"}
		child := &Comment{ID: 2, ParentID: 1, Body: "child"}
		flat := []*Comment{root, child, root, child}
		roots := BuildCommentTree(flat)
		if len(roots) != 1 {
			t.Fatalf("roots = %d, want 1", len(roots))
		}
		if len(roots[0].Children) != 1 {
			t.Fatalf("children = %d, want 1 (duplicates collapsed)", len(roots[0].Children))
		}
	})

	t.Run("idempotent on re-run", func(t *testing.T) {
		// Calling BuildCommentTree twice on the same slice must not double-append.
		flat := []*Comment{
			{ID: 1, ParentID: 0},
			{ID: 2, ParentID: 1},
		}
		_ = BuildCommentTree(flat)
		roots := BuildCommentTree(flat)
		if len(roots) != 1 || len(roots[0].Children) != 1 {
			t.Fatalf("second build: roots=%d children=%d, want 1/1", len(roots), len(roots[0].Children))
		}
	})

	t.Run("self-parent treated as root", func(t *testing.T) {
		flat := []*Comment{{ID: 5, ParentID: 5, Body: "self"}}
		roots := BuildCommentTree(flat)
		if len(roots) != 1 || roots[0].ID != 5 {
			t.Fatalf("expected single self-root, got %+v", roots)
		}
	})

	t.Run("parent cycle does not drop comments or loop", func(t *testing.T) {
		// A↔B mutual-parent cycle: both must survive as roots and the tree must
		// be acyclic (CountComments must terminate and count both).
		flat := []*Comment{
			{ID: 1, ParentID: 2, Body: "A"},
			{ID: 2, ParentID: 1, Body: "B"},
		}
		roots := BuildCommentTree(flat)
		if len(roots) != 2 {
			t.Fatalf("roots = %d, want 2 (cycle members promoted to roots)", len(roots))
		}
		if n := CountComments(roots); n != 2 {
			t.Errorf("CountComments = %d, want 2", n)
		}
		// Neither may list the other as a child (that would re-form the cycle).
		for _, r := range roots {
			if len(r.Children) != 0 {
				t.Errorf("root %d has %d children, want 0 (cycle broken)", r.ID, len(r.Children))
			}
		}
	})
}

func TestCountCommentsCycleSafe(t *testing.T) {
	// Build a cyclic tree directly (bypass BuildCommentTree) and confirm
	// CountComments terminates and does not stack-overflow.
	a := &Comment{ID: 1}
	b := &Comment{ID: 2}
	a.Children = []*Comment{b}
	b.Children = []*Comment{a} // cycle
	n := CountComments([]*Comment{a})
	if n != 2 {
		t.Errorf("CountComments on cyclic tree = %d, want 2", n)
	}
}

func TestCountComments(t *testing.T) {
	tree := []*Comment{
		{
			ID:   1,
			Body: "root",
			Children: []*Comment{
				{ID: 2, Body: "child1"},
				{
					ID:   3,
					Body: "child2",
					Children: []*Comment{
						{ID: 4, Body: "grandchild"},
					},
				},
			},
		},
		{ID: 5, Body: "root2"},
	}
	if got := CountComments(tree); got != 5 {
		t.Errorf("CountComments = %d, want 5", got)
	}
}

func TestCountCommentsEmpty(t *testing.T) {
	if got := CountComments(nil); got != 0 {
		t.Errorf("CountComments(nil) = %d, want 0", got)
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			name: "month day year time",
			in:   "March 13 2007, 23:59",
			want: time.Date(2007, 3, 13, 23, 59, 0, 0, time.UTC),
		},
		{
			name: "with seconds and timezone",
			in:   "February 26 2018, 11:03:58 UTC",
			want: time.Date(2018, 2, 26, 11, 3, 58, 0, time.UTC),
		},
		{
			name: "zero-padded day",
			in:   "January 05 2020, 08:30",
			want: time.Date(2020, 1, 5, 8, 30, 0, 0, time.UTC),
		},
		{
			name: "iso format",
			in:   "2023-06-15 14:30:00",
			want: time.Date(2023, 6, 15, 14, 30, 0, 0, time.UTC),
		},
		{
			name: "invalid returns zero",
			in:   "not a date",
			want: time.Time{},
		},
		{
			name: "empty returns zero",
			in:   "",
			want: time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDate(tt.in)
			if !got.Equal(tt.want) {
				t.Errorf("ParseDate(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
