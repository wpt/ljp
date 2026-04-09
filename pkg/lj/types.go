package lj

import "time"

type Post struct {
	ID       int        `json:"id"`
	URL      string     `json:"url"`
	Title    string     `json:"title"`
	Date     string     `json:"date"`
	DateUnix int64      `json:"date_unix,omitempty"`
	Author   string     `json:"author"`
	Body     string     `json:"body"`
	Tags     []string   `json:"tags,omitempty"`
	OG       *OGMeta    `json:"og,omitempty"`
	Comments []*Comment `json:"comments,omitempty"`
}

type OGMeta struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
}

type Comment struct {
	ID       int        `json:"id"`
	TalkID   int        `json:"talk_id"`
	ParentID int        `json:"parent_id"`
	Level    int        `json:"level"`
	Author   string     `json:"author"`
	Username string     `json:"username"`
	Date     string     `json:"date"`
	DateUnix int64      `json:"date_unix"`
	Body     string     `json:"body"`
	Subject  string     `json:"subject,omitempty"`
	Userpic  string     `json:"userpic,omitempty"`
	Deleted  bool       `json:"deleted,omitempty"`
	Children []*Comment `json:"children,omitempty"`
}

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
	Thread  int    `json:"thread"`
	Loaded  int    `json:"loaded"`
}

type sitePage struct {
	ReplyCount int          `json:"replycount"`
	Comments   []rawComment `json:"comments"`
}

func BuildCommentTree(flat []*Comment) []*Comment {
	byID := make(map[int]*Comment, len(flat))
	for _, c := range flat {
		byID[c.ID] = c
	}
	var roots []*Comment
	for _, c := range flat {
		if c.ParentID == 0 {
			roots = append(roots, c)
		} else if parent, ok := byID[c.ParentID]; ok {
			parent.Children = append(parent.Children, c)
		} else {
			roots = append(roots, c)
		}
	}
	return roots
}

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
