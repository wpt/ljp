package lj

import (
	"strings"
	"testing"
)

func TestParsePostURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		user    string
		id      int
		wantErr bool
	}{
		{
			name: "standard URL",
			url:  "https://news.livejournal.com/166511.html",
			user: "news",
			id:   166511,
		},
		{
			name: "URL with query params",
			url:  "https://news.livejournal.com/166511.html?page=2&format=light",
			user: "news",
			id:   166511,
		},
		{
			name: "URL with trailing slash",
			url:  "https://news.livejournal.com/166511.html/",
			user: "news",
			id:   166511,
		},
		{
			name: "URL with whitespace",
			url:  "  https://news.livejournal.com/166511.html  ",
			user: "news",
			id:   166511,
		},
		{
			name:    "not livejournal",
			url:     "https://example.com/166511.html",
			wantErr: true,
		},
		{
			name:    "malformed URL",
			url:     "not-a-url",
			wantErr: true,
		},
		{
			name:    "no post ID",
			url:     "https://news.livejournal.com/",
			wantErr: true,
		},
		{
			name:    "non-numeric post ID",
			url:     "https://news.livejournal.com/profile",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, id, err := ParsePostURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got user=%q id=%d", user, id)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user != tt.user {
				t.Errorf("user = %q, want %q", user, tt.user)
			}
			if id != tt.id {
				t.Errorf("id = %d, want %d", id, tt.id)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		html    string
		want    string
		wantErr bool
	}{
		{
			name: "simple object",
			html: `<script>Site.page = {"replycount":5,"comments":[]};</script>`,
			want: `{"replycount":5,"comments":[]}`,
		},
		{
			name: "nested braces",
			html: `<script>Site.page = {"a":{"b":{"c":1}}};</script>`,
			want: `{"a":{"b":{"c":1}}}`,
		},
		{
			name: "braces in strings",
			html: `<script>Site.page = {"text":"hello {world}"};</script>`,
			want: `{"text":"hello {world}"}`,
		},
		{
			name: "escaped quotes in strings",
			html: `<script>Site.page = {"text":"say \"hi\""};</script>`,
			want: `{"text":"say \"hi\""}`,
		},
		{
			name: "no trailing semicolon",
			html: `<script>Site.page = {"ok":true}</script>`,
			want: `{"ok":true}`,
		},
		{
			name:    "no Site.page",
			html:    `<script>var x = 1;</script>`,
			wantErr: true,
		},
		{
			name:    "unmatched braces",
			html:    `<script>Site.page = {"broken":true</script>`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractJSON([]byte(tt.html))
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractSitePage(t *testing.T) {
	html := []byte(`<script>Site.page = {"replycount":2,"comments":[{"article":"hello","uname":"user1","dname":"User One","talkid":100,"dtalkid":200,"parent":0,"level":1,"ctime":"January 1 2020, 12:00:00 UTC","ctime_ts":1577836800,"subject":"","userpic":"","deleted":0,"loaded":1,"thread":200},{"article":"reply","uname":"user2","dname":"User Two","talkid":101,"dtalkid":201,"parent":200,"level":2,"ctime":"January 1 2020, 13:00:00 UTC","ctime_ts":1577840400,"subject":"re","userpic":"","deleted":0,"loaded":1,"thread":201}]};</script>`)

	sp, err := extractSitePage(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sp.ReplyCount != 2 {
		t.Errorf("replycount = %d, want 2", sp.ReplyCount)
	}
	if len(sp.Comments) != 2 {
		t.Fatalf("comments count = %d, want 2", len(sp.Comments))
	}
	if sp.Comments[0].DName != "User One" {
		t.Errorf("first comment dname = %q, want %q", sp.Comments[0].DName, "User One")
	}
	if sp.Comments[1].Parent != 200 {
		t.Errorf("second comment parent = %d, want 200", sp.Comments[1].Parent)
	}
}

func TestParseJournalIndex(t *testing.T) {
	html := `<html><body>
		<a href="/12345.html">Post 1</a>
		<a href="/67890.html">Post 2</a>
		<a href="/12345.html">Post 1 duplicate</a>
		<a href="https://other.livejournal.com/99999.html">Other journal</a>
		<a href="/tag/sometag">Not a post</a>
		<a href="/555.html">Post 3</a>
	</body></html>`

	ids, err := ParseJournalIndex(strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect 12345, 67890, 99999 (from other journal link — regex matches any /N.html), 555
	// Deduplication removes the second 12345
	if len(ids) != 4 {
		t.Fatalf("ids count = %d, want 4, got %v", len(ids), ids)
	}
	if ids[0] != 12345 {
		t.Errorf("ids[0] = %d, want 12345", ids[0])
	}
	if ids[1] != 67890 {
		t.Errorf("ids[1] = %d, want 67890", ids[1])
	}
}

func TestParseJournalIndexEmpty(t *testing.T) {
	html := `<html><body><p>No posts here</p></body></html>`
	ids, err := ParseJournalIndex(strings.NewReader(html))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 ids, got %d", len(ids))
	}
}
