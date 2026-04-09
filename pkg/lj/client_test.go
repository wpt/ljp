package lj

import "testing"

func TestPostURL(t *testing.T) {
	c := NewClient()
	got := c.postURL("news", 166511)
	want := "https://news.livejournal.com/166511.html"
	if got != want {
		t.Errorf("postURL = %q, want %q", got, want)
	}
}

func TestCommentsURL(t *testing.T) {
	c := NewClient()
	got := c.commentsURL("news", 166511, 3)
	want := "https://news.livejournal.com/166511.html?view=flat&page=3&format=light"
	if got != want {
		t.Errorf("commentsURL = %q, want %q", got, want)
	}
}
