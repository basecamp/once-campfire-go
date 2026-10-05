package web

import "testing"

// The reference's presenters::pagination tests.
func TestGearedPagesLikeTheReference(t *testing.T) {
	page := newGearedPage("2", 1005, []int64{500})
	if page.offset() != 500 || page.limit() != 500 || page.pageCount() != 3 || page.number == page.pageCount() {
		t.Fatal(page.offset(), page.limit(), page.pageCount())
	}
	if last := newGearedPage("3", 1005, []int64{500}); last.number != last.pageCount() {
		t.Fatal("page 3 of 1005 by 500 is the last")
	}
	if empty := newGearedPage("", 0, []int64{20}); empty.number != empty.pageCount() {
		t.Fatal("an empty recordset's first page is the last")
	}
	for _, test := range []struct {
		param         string
		offset, limit int64
	}{{"1", 0, 15}, {"3", 45, 50}, {"5", 195, 100}, {"6", 295, 100}} {
		page := newGearedPage(test.param, 1000, []int64{15, 30, 50, 100})
		if page.offset() != test.offset || page.limit() != test.limit {
			t.Fatal(test.param, page.offset(), page.limit())
		}
	}
	for param, number := range map[string]int64{"abc": 1, "-2": 1, " 2x": 2, "1_0": 10, "99999999999999999999": 1_000_000_000} {
		if page := newGearedPage(param, 10, []int64{5}); page.number != number {
			t.Fatal(param, page.number)
		}
	}
}

func TestNextPageLinksMergeThePageIntoSortedQueryValues(t *testing.T) {
	for url, next := range map[string]string{
		"http://x.test/autocompletable/users.json":                            "http://x.test/autocompletable/users.json?page=2",
		"http://x.test/autocompletable/users.json?query=a+b&page=1&room_id=3": "http://x.test/autocompletable/users.json?page=2&query=a%20b&room_id=3",
		"http://x.test/p?query=a%2Bb":                                         "http://x.test/p?page=2&query=a%2Bb",
		"http://x.test/p?q=a%2B+b&page=1":                                     "http://x.test/p?page=2&q=a%2B%20b",
		"http://x.test/p?a+b=c+d":                                             "http://x.test/p?a%2Bb=c%20d&page=2",
		"http://x.test/p?k=%20+":                                              "http://x.test/p?k=%20%20&page=2",
		"http://x.test/p?k":                                                   "http://x.test/p?k&page=2",
	} {
		if got := urlWithPage(url, "2"); got != next {
			t.Errorf("%s: %s", url, got)
		}
	}
}

func TestRoomCreationRestrictionIsPresenceOfTheSetting(t *testing.T) {
	for settings, restricted := range map[string]bool{
		`{}`: false, `{"restrict_room_creation_to_administrators":false}`: false,
		`{"restrict_room_creation_to_administrators":true}`: true, `{"restrict_room_creation_to_administrators":" "}`: false,
		`{"restrict_room_creation_to_administrators":"0"}`: true, `{"restrict_room_creation_to_administrators":0}`: true,
		`{"restrict_room_creation_to_administrators":null}`: false, `not json`: false,
	} {
		if restrictRoomCreation([]byte(settings)) != restricted {
			t.Error(settings)
		}
	}
}
