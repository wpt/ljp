package lj

import "time"

// Post is a parsed LiveJournal entry. Body format is controlled by
// Client.BodyFormat (see FormatHTML, FormatMarkdown, FormatText). Comments is
// nil until the caller stitches in ParseComments output — ParsePost itself
// never populates it.
type Post struct {
	ID    int    `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	// Date is the timestamp as displayed by LJ. Use ParseDate to convert.
	Date string `json:"date"`
	// DateUnix is seconds since epoch, or 0 if Date could not be parsed. LJ's
	// displayed timestamp usually carries no zone, so a zoneless Date is
	// interpreted as UTC — treat DateUnix as accurate to within the journal's
	// timezone offset, not to the second. Comment timestamps come from LJ's own
	// epoch (Comment.DateUnix) and are exact.
	DateUnix int64    `json:"date_unix,omitzero"`
	Author   string   `json:"author"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags,omitempty"`
	// ReplyCount is what LJ's Site.page reports — best-effort, may be zero on
	// malformed pages.
	ReplyCount int `json:"reply_count,omitzero"`
	// OG holds OpenGraph <meta property="og:..."> values, nil if none found.
	OG *OGMeta `json:"og,omitzero"`
	// Comments is nil until you assign ParseComments output, which is already a
	// built tree — do not run BuildCommentTree on it again.
	Comments []*Comment `json:"comments,omitempty"`
}

// OGMeta is OpenGraph metadata extracted from the post's <meta property="og:*">.
type OGMeta struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
}

// Comment is one LiveJournal comment. ID is the canonical dtalkid used in
// reply URLs; TalkID is the internal talkid (rarely useful for callers).
// Author is the human-readable display name (LJ DName); Username is the
// LJ login slug (LJ UName) — Username is the stable identifier, Author may
// contain Unicode display characters.
type Comment struct {
	ID       int    `json:"id"`
	TalkID   int    `json:"talk_id"`
	ParentID int    `json:"parent_id"`
	Level    int    `json:"level"` // depth in the comment tree, 1-based
	Author   string `json:"author"`
	Username string `json:"username"`
	Date     string `json:"date"`
	DateUnix int64  `json:"date_unix,omitzero"`
	Body     string `json:"body"`
	Subject  string `json:"subject,omitempty"`
	Userpic  string `json:"userpic,omitempty"`
	Deleted  bool   `json:"deleted,omitzero"`
	// Children holds nested replies. ParseComments returns an already-built tree
	// (it calls BuildCommentTree internally), so Children is populated there.
	// Run BuildCommentTree yourself only on a flat list you assembled — never on
	// ParseComments output, or the nesting is flattened away.
	Children []*Comment `json:"children,omitempty"`
}

// rawComment is the JSON shape inside Site.page.comments. We previously also
// parsed `thread` and `loaded` to bridge the threaded view (where collapsed
// comments had empty bodies); after the flat-view-only migration both are dead.
type rawComment struct {
	Article string `json:"article"`
	UName   string `json:"uname"`
	DName   string `json:"dname"`
	TalkID  int    `json:"talkid"`
	DTalkID int    `json:"dtalkid"`
	Parent  int    `json:"parent"`
	Level   int    `json:"level"`
	CTime   string `json:"ctime"`
	CTimeTS int64  `json:"ctime_ts"`
	Subject string `json:"subject"`
	Userpic string `json:"userpic"`
	Deleted int    `json:"deleted"`
}

type sitePage struct {
	ReplyCount int          `json:"replycount"`
	Comments   []rawComment `json:"comments"`
}

// BuildCommentTree converts a flat comment list into a tree via ParentID links.
// It is idempotent: calling it twice on the same slice (or on a slice with
// duplicate IDs from LJ's 'last page returns forever' quirk) produces the same
// result as a single call. Children slices on input comments are cleared
// before reattachment. Comments whose parent is not present in flat are
// promoted to roots (LJ's flat view occasionally leaves orphans). Comments
// whose ParentID links would form a cycle are also promoted to roots so the
// resulting tree is always acyclic (a cyclic Children graph would make
// CountComments-style walkers and RenderPost loop).
func BuildCommentTree(flat []*Comment) []*Comment {
	byID := make(map[int]*Comment, len(flat))
	unique := make([]*Comment, 0, len(flat))
	for _, c := range flat {
		if c == nil || byID[c.ID] != nil {
			continue
		}
		byID[c.ID] = c
		c.Children = nil // reset so re-runs don't double-append
		unique = append(unique, c)
	}
	var roots []*Comment
	for _, c := range unique {
		parent, ok := byID[c.ParentID]
		if c.ParentID == 0 || c.ParentID == c.ID || !ok || formsCycle(c, parent, byID) {
			roots = append(roots, c) // root, self-parent, orphan, or would-be cycle
			continue
		}
		parent.Children = append(parent.Children, c)
	}
	return roots
}

// formsCycle reports whether attaching c under parent would create a cycle —
// i.e. c is already an ancestor of parent via the ParentID chain. Bounded by
// the node count so a pre-existing cycle in the chain can't loop forever.
func formsCycle(c, parent *Comment, byID map[int]*Comment) bool {
	for steps := 0; parent != nil && steps <= len(byID); steps++ {
		if parent == c {
			return true
		}
		if parent.ParentID == 0 || parent.ParentID == parent.ID {
			return false
		}
		next, ok := byID[parent.ParentID]
		if !ok {
			return false
		}
		parent = next
	}
	return true // chain longer than the node count ⇒ already cyclic
}

// CountComments returns the total number of comments in a tree (including
// nested children). Iterative with a visited set so a cyclic/malformed tree
// cannot stack-overflow or loop forever.
func CountComments(comments []*Comment) int {
	visited := make(map[*Comment]bool)
	stack := append([]*Comment(nil), comments...)
	n := 0
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if c == nil || visited[c] {
			continue
		}
		visited[c] = true
		n++
		stack = append(stack, c.Children...)
	}
	return n
}

// ParseDate parses LJ-formatted timestamps in any of the known display
// formats. Returns the zero time.Time on failure — callers should check
// result.IsZero() to distinguish "missing/unparseable" from a real timestamp.
func ParseDate(s string) time.Time {
	formats := []string{
		"January 2 2006, 15:04",
		"January 2 2006, 15:04:05 MST",
		"January 02 2006, 15:04",
		"January 02 2006, 15:04:05 MST",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
