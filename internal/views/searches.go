package views

// Views for reference/app/views/searches, plus SearchesHelper (the reference's searches.rs).

// SearchIndexView is what searches/index shows.
type SearchIndexView struct {
	// @query: params[:q] with non-word characters turned into spaces, when present.
	Query *string
	// params[:q] as submitted, the search field's value.
	Q *string
	// Current.user.reachable_messages.search(query).last(100).
	Messages []MessageItem
	// Current.user.searches.ordered.pluck(:query).
	RecentSearches []string
	// last_room_visited.id, where the exit button goes.
	ReturnToRoomID int64
}

// SearchesIndex is searches/index.
type SearchesIndex struct {
	PageBase
	Ctx   *ViewContext
	Index *SearchIndexView
}

func (p *SearchesIndex) PageTitle() *string { return Ptr("Search") }
func (p *SearchesIndex) BodyClass() *string { return Ptr("sidebar searches") }

// SearchPath is searches_path(q: query).
func SearchPath(query string) string { return RouteSearches() + "?q=" + CGIEscape(query) }
