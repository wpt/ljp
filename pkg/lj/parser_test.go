package lj

import (
	"log/slog"
	"strings"
	"testing"
)

func TestDetectMaxPage(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	tests := []struct {
		name string
		html string
		want int
	}{
		{
			name: "no pagination links",
			html: `<html><body><p>nothing</p></body></html>`,
			want: 1,
		},
		{
			name: "single page=2 link with format=light",
			html: `<html><body><a href="?page=2&format=light">2</a></body></html>`,
			want: 2,
		},
		{
			name: "multiple pages with format=light",
			html: `<html><body>
				<a href="?page=2&format=light">2</a>
				<a href="?page=3&format=light">3</a>
				<a href="?page=5&format=light">5</a>
			</body></html>`,
			want: 5,
		},
		{
			name: "page=N without format=light is ignored",
			html: `<html><body><a href="?page=99">share</a></body></html>`,
			want: 1,
		},
		{
			name: "raw text mentioning ?page=99 is ignored",
			html: `<html><body><p>see ?page=99&format=light</p></body></html>`,
			want: 1,
		},
		{
			name: "malformed page= ignored",
			html: `<html><body><a href="?page=abc&format=light">x</a></body></html>`,
			want: 1,
		},
		{
			name: "non-pagination links with page query are ignored if format mismatches",
			html: `<html><body>
				<a href="?page=10&format=html">decoy</a>
				<a href="?page=4&format=light">real</a>
			</body></html>`,
			want: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectMaxPage([]byte(tt.html), log)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDetectMaxPageCap(t *testing.T) {
	// Anything beyond maxCommentPages must be clamped.
	log := slog.New(slog.DiscardHandler)
	html := `<html><body><a href="?page=99999&format=light">x</a></body></html>`
	if got := detectMaxPage([]byte(html), log); got != maxCommentPages {
		t.Errorf("got %d, want %d (cap)", got, maxCommentPages)
	}
}

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
			name:    "livejournal lookalike host",
			url:     "https://news.livejournal.com.evil.test/166511.html",
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
		{
			name: "users.livejournal.com path form (underscore journal)",
			url:  "https://users.livejournal.com/some_user/12345.html",
			user: "some_user",
			id:   12345,
		},
		{
			name:    "www is not a journal subdomain",
			url:     "https://www.livejournal.com/12345.html",
			wantErr: true,
		},
		{
			name:    "users subdomain without path is rejected",
			url:     "https://users.livejournal.com/12345.html",
			wantErr: true,
		},
		{
			name:    "negative post ID rejected",
			url:     "https://news.livejournal.com/-5.html",
			wantErr: true,
		},
		{
			name:    "zero post ID rejected",
			url:     "https://news.livejournal.com/0.html",
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
		{
			name: "earlier mention without equals is ignored",
			html: `<script>var s = "Site.page is a thing"; Site.page = {"ok":true};</script>`,
			want: `{"ok":true}`,
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

func TestExtractSitePagePrefersRealScript(t *testing.T) {
	// The rendered post body quotes the LJ marker with a valid-but-empty object;
	// the genuine Site.page (with comments) appears later. extractSitePage must
	// pick the latter, not the first occurrence.
	html := []byte(`<div class="aentry-post__text">Tutorial: write Site.page = {"foo":1} in your S2 style.</div>` +
		`<script>Site.page = {"replycount":1,"comments":[` +
		`{"article":"real","uname":"u","dname":"U","talkid":1,"dtalkid":10,"parent":0,"level":1,"ctime":"","ctime_ts":0,"deleted":0}` +
		`]};</script>`)
	sp, err := extractSitePage(html)
	if err != nil {
		t.Fatalf("extractSitePage: %v", err)
	}
	if len(sp.Comments) != 1 {
		t.Fatalf("comments = %d, want 1 (real script, not the quoted decoy)", len(sp.Comments))
	}
	if sp.Comments[0].Article != "real" {
		t.Errorf("article = %q, want %q", sp.Comments[0].Article, "real")
	}
}

func TestParseJournalIndex(t *testing.T) {
	// Real LiveJournal index/archive pages link posts as ABSOLUTE same-journal
	// URLs (https://<user>.livejournal.com/<id>.html). This fixture mirrors that
	// plus the noise links that must be rejected.
	const host = "news.livejournal.com"
	html := `<html><body>
		<a href="https://news.livejournal.com/12345.html">Post 1 (absolute, same journal)</a>
		<a href="https://news.livejournal.com/67890.html?view=comments">Post 2 (absolute + query)</a>
		<a href="https://news.livejournal.com/12345.html#cutid1">Post 1 duplicate (fragment)</a>
		<a href="/555.html">Post 3 (relative, still accepted)</a>
		<a href="https://other.livejournal.com/99999.html">Other journal (rejected: foreign host)</a>
		<a href="https://news.livejournal.com/63887.html?thread=104856463#t1">Recent comment (rejected: ?thread=)</a>
		<a href="https://news.livejournal.com/tag/foo">Tag page (rejected: /tag/)</a>
		<a href="https://www.livejournal.com/update.bml?repost=https://news.livejournal.com/12345.html">Repost (rejected)</a>
	</body></html>`

	ids, err := ParseJournalIndex(strings.NewReader(html), host)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 12345, 67890, 555 — foreign host, ?thread=, and /tag/ are filtered.
	want := []int{12345, 67890, 555}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], w)
		}
	}
}

func TestParseJournalIndexEmpty(t *testing.T) {
	html := `<html><body><p>No posts here</p></body></html>`
	ids, err := ParseJournalIndex(strings.NewReader(html), "news.livejournal.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 ids, got %d", len(ids))
	}
}
