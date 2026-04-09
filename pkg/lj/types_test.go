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
