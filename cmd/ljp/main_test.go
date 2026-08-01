package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImagesRefFor(t *testing.T) {
	cases := []struct {
		name                string
		images, dir, output string
		want                string
	}{
		// The documented recipe: `--images ./img --dir ./posts`. posts/{id}.html
		// must reach up and over, or every image 404s in the browser.
		{"sibling dirs", "img", "posts", "", filepath.FromSlash("../img")},
		{"dotted sibling", "./img", "./posts", "", filepath.FromSlash("../img")},
		// Images nested inside the output dir resolve straight down.
		{"nested in dir", filepath.Join("posts", "img"), "posts", "", "img"},
		// Same dir for both: no prefix at all.
		{"same dir", "posts", "posts", "", "."},
		// -o names a file; the document dir is its parent.
		{"output file", "img", "", filepath.Join("out", "p.html"), filepath.FromSlash("../img")},
		{"output in cwd", "img", "", "p.html", "img"},
		// Streaming to stdout: no document location, so cwd-relative is all
		// that's meaningful — "" means "fall back to ImagesDir".
		{"stdout stream", "img", "", "", ""},
		// No --images at all.
		{"no images", "", "posts", "", ""},
		// --dir wins over -o (they're mutually exclusive anyway).
		{"dir beats output", "img", "posts", filepath.Join("out", "p.html"), filepath.FromSlash("../img")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imagesRefFor(tc.images, tc.dir, tc.output); got != tc.want {
				t.Errorf("imagesRefFor(%q, %q, %q) = %q, want %q",
					tc.images, tc.dir, tc.output, got, tc.want)
			}
		})
	}
}

// Either side of the pair can be absolute while the other is relative — both
// mixes come straight off the command line, and filepath.Rel refuses such a
// pair outright. Falling back to a cwd-relative ImagesDir there would reproduce
// the very bug ImagesRef exists to fix, so imagesRefFor absolutises first.
func TestImagesRefForMixedAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	// Not t.TempDir() directly: on macOS it hands back a /var symlink while
	// filepath.Abs resolves through to /private/var, and the two wouldn't
	// relativise against each other.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	want := filepath.FromSlash("../img")

	t.Run("absolute images, relative dir", func(t *testing.T) {
		if got := imagesRefFor(filepath.Join(cwd, "img"), "posts", ""); got != want {
			t.Errorf("imagesRefFor(abs img, %q, \"\") = %q, want %q", "posts", got, want)
		}
	})
	t.Run("relative images, absolute dir", func(t *testing.T) {
		if got := imagesRefFor("img", filepath.Join(cwd, "posts"), ""); got != want {
			t.Errorf("imagesRefFor(%q, abs dir, \"\") = %q, want %q", "img", got, want)
		}
	})
	t.Run("both absolute", func(t *testing.T) {
		if got := imagesRefFor(filepath.Join(cwd, "img"), filepath.Join(cwd, "posts"), ""); got != want {
			t.Errorf("imagesRefFor(abs, abs, \"\") = %q, want %q", got, want)
		}
	})
}

func TestScanExistingPostsMissingDir(t *testing.T) {
	ids, err := scanExistingPosts(filepath.Join(t.TempDir(), "does-not-exist"), ".json")
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("want empty map, got %v", ids)
	}
}

func TestScanExistingPostsEmpty(t *testing.T) {
	ids, err := scanExistingPosts(t.TempDir(), ".json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("want empty map, got %v", ids)
	}
}

func TestScanExistingPostsMixed(t *testing.T) {
	dir := t.TempDir()
	// Good files.
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "42.html"), []byte("<html/>"), 0644); err != nil {
		t.Fatal(err)
	}
	// 0-byte file from a crashed run — must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	// Unrelated files — must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	// Subdirectory with a JSON file inside — must not be picked up.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "99.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// Only the current output format counts: a .json archive must not suppress
	// a --render (.html) run, and vice versa.
	ids, err := scanExistingPosts(dir, ".json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || !ids[1] {
		t.Errorf("json scan = %v, want {1}", ids)
	}
	if ids[7] {
		t.Error("0-byte file 7.json should not be marked done")
	}
	if ids[99] {
		t.Error("subdir file 99.json should not be marked done")
	}

	htmlIDs, err := scanExistingPosts(dir, ".html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(htmlIDs) != 1 || !htmlIDs[42] {
		t.Errorf("html scan = %v, want {42}", htmlIDs)
	}
}

func TestFilterSkipped(t *testing.T) {
	tests := []struct {
		name string
		ids  []int
		skip map[int]bool
		want []int
	}{
		{name: "nil skip", ids: []int{1, 2, 3}, skip: nil, want: []int{1, 2, 3}},
		{name: "empty skip", ids: []int{1, 2, 3}, skip: map[int]bool{}, want: []int{1, 2, 3}},
		{name: "partial", ids: []int{1, 2, 3, 4}, skip: map[int]bool{2: true, 4: true}, want: []int{1, 3}},
		{name: "all skipped", ids: []int{1, 2}, skip: map[int]bool{1: true, 2: true}, want: []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterSkipped(tt.ids, tt.skip)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, v := range tt.want {
				if got[i] != v {
					t.Errorf("got[%d] = %d, want %d", i, got[i], v)
				}
			}
		})
	}
}

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
		{name: "explicit zero id rejected", arg: "news/0", wantErr: true},
		{name: "negative id rejected", arg: "news/-5", wantErr: true},
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

func TestParseSelector(t *testing.T) {
	tests := []struct {
		name     string
		arg      string
		kind     selectorKind
		ordinals []int
		ljIDs    []int
		dates    []int
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
		{name: "zero lj id", arg: "@0", wantErr: true},
		{name: "negative lj id in list", arg: "@0,@5", wantErr: true},
		{name: "zero ordinal", arg: "0", wantErr: true},
		{name: "zero ordinal range start", arg: "0-5", wantErr: true},
		{name: "date single month", arg: "2019/05", kind: selDateRange, dates: []int{2019, 5, 2019, 5}},
		{name: "date range", arg: "2019/05-2020/01", kind: selDateRange, dates: []int{2019, 5, 2020, 1}},
		{name: "date inverted", arg: "2020/01-2019/05", wantErr: true},
		{name: "date bad month", arg: "2019/13", wantErr: true},
		{name: "date bad year", arg: "19/05", wantErr: true},
		{name: "date missing month", arg: "2019/", wantErr: true},
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
			if tt.dates != nil {
				if len(sel.dates) != len(tt.dates) {
					t.Fatalf("dates = %v, want %v", sel.dates, tt.dates)
				}
				for i, v := range tt.dates {
					if sel.dates[i] != v {
						t.Errorf("dates[%d] = %d, want %d", i, sel.dates[i], v)
					}
				}
			}
		})
	}
}
