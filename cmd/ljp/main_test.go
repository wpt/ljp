package main

import (
	"testing"

	"github.com/wpt/ljp/pkg/lj"
)

func TestParseArg(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		user    string
		id      int
		wantErr bool
	}{
		{name: "user/id", arg: "news/166511", user: "news", id: 166511},
		{name: "url", arg: "https://news.livejournal.com/166511.html", user: "news", id: 166511},
		{name: "username only", arg: "news", user: "news", id: 0},
		{name: "bad id", arg: "news/abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, id, err := parseArg(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user != tt.user || id != tt.id {
				t.Errorf("got %s/%d, want %s/%d", user, id, tt.user, tt.id)
			}
		})
	}
}

func TestCountComments(t *testing.T) {
	tree := []*lj.Comment{
		{
			ID:   1,
			Body: "root",
			Children: []*lj.Comment{
				{ID: 2, Body: "child1"},
				{
					ID:   3,
					Body: "child2",
					Children: []*lj.Comment{
						{ID: 4, Body: "grandchild"},
					},
				},
			},
		},
		{ID: 5, Body: "root2"},
	}
	got := countComments(tree)
	if got != 5 {
		t.Errorf("countComments = %d, want 5", got)
	}
}

func TestCountCommentsEmpty(t *testing.T) {
	got := countComments(nil)
	if got != 0 {
		t.Errorf("countComments(nil) = %d, want 0", got)
	}
}

func TestParseSelector(t *testing.T) {
	tests := []struct {
		name     string
		arg      string
		kind     selectorKind
		ordinals []int
		ljIDs    []int
		wantErr  bool
	}{
		{name: "ordinal range", arg: "1-222", kind: selOrdinalRange, ordinals: []int{1, 222}},
		{name: "ordinal list", arg: "1,33,444", kind: selOrdinalList, ordinals: []int{1, 33, 444}},
		{name: "single ordinal", arg: "5", kind: selOrdinalList, ordinals: []int{5}},
		{name: "lj id", arg: "@166511", kind: selLJIDList, ljIDs: []int{166511}},
		{name: "lj id list", arg: "@256,@166511", kind: selLJIDList, ljIDs: []int{256, 166511}},
		{name: "lj id range", arg: "@256-@100000", kind: selLJIDRange, ljIDs: []int{256, 100000}},
		{name: "bad ordinal", arg: "abc", wantErr: true},
		{name: "bad lj id", arg: "@abc", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := parseSelector(tt.arg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sel.kind != tt.kind {
				t.Errorf("kind = %d, want %d", sel.kind, tt.kind)
			}
			if tt.ordinals != nil {
				if len(sel.ordinals) != len(tt.ordinals) {
					t.Fatalf("ordinals = %v, want %v", sel.ordinals, tt.ordinals)
				}
				for i, v := range tt.ordinals {
					if sel.ordinals[i] != v {
						t.Errorf("ordinals[%d] = %d, want %d", i, sel.ordinals[i], v)
					}
				}
			}
			if tt.ljIDs != nil {
				if len(sel.ljIDs) != len(tt.ljIDs) {
					t.Fatalf("ljIDs = %v, want %v", sel.ljIDs, tt.ljIDs)
				}
				for i, v := range tt.ljIDs {
					if sel.ljIDs[i] != v {
						t.Errorf("ljIDs[%d] = %d, want %d", i, sel.ljIDs[i], v)
					}
				}
			}
		})
	}
}
