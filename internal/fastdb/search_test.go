package fastdb

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/database"
)

// TestDifferentialSearch pins fastdb.Search against database.DB.Search on the
// shared fixture: every user, a corpus of queries covering the seed's words,
// empty results, membership filtering and quoted tokens. The fixture query set
// is deterministic: fixed edge cases plus words sampled from the FTS index
// itself, so the test exercises the real corpus whether the parity seed or the
// synthetic fallback is in use.
func TestDifferentialSearch(t *testing.T) {
	path := fixtureDB(t)
	d, c := openBoth(t, path)
	ctx := context.Background()

	users, err := queryIDs(t, d, "SELECT id FROM users ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) == 0 {
		t.Fatal("fixture has no users")
	}

	queries := searchFixtureQueries(t, d)
	for _, user := range users {
		for _, query := range queries {
			want, wantErr := d.Search(ctx, user, query)
			got, gotErr := c.Search(nil, user, query)
			if (wantErr == nil) != (gotErr == nil) {
				t.Errorf("Search(%d,%q): fastdb err %v != database err %v", user, query, gotErr, wantErr)
				continue
			}
			if wantErr != nil {
				continue
			}
			if (want == nil) != (got == nil) {
				t.Errorf("Search(%d,%q): nil-ness fastdb %v != database %v", user, query, got == nil, want == nil)
				continue
			}
			if len(got) != len(want) {
				t.Errorf("Search(%d,%q): fastdb %d rows != database %d", user, query, len(got), len(want))
				continue
			}
			for i := range want {
				if !reflect.DeepEqual(got[i].record(), recordOfMessage(want[i])) {
					t.Errorf("Search(%d,%q)[%d]: fastdb %+v != database %+v", user, query, i, got[i].record(), recordOfMessage(want[i]))
				}
			}
		}
	}

	// A user with no memberships must see nothing, on both readers.
	const missing = int64(1) << 62
	for _, query := range queries {
		want, err := d.Search(ctx, missing, query)
		if err != nil {
			t.Fatalf("database.Search(%d,%q): %v", missing, query, err)
		}
		got, err := c.Search(nil, missing, query)
		if err != nil {
			t.Fatalf("fastdb.Search(%d,%q): %v", missing, query, err)
		}
		if len(got) != len(want) {
			t.Errorf("Search(%d,%q) leaked rows: fastdb %d != database %d", missing, query, len(got), len(want))
		}
	}
}

// searchFixtureQueries builds the deterministic query corpus: edge cases plus
// words sampled from the fixture's own FTS index bodies.
func searchFixtureQueries(t *testing.T, d *database.DB) []string {
	t.Helper()
	ctx := context.Background()
	var words []string
	rows, err := d.Read.QueryContext(ctx, "SELECT body FROM message_search_index")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		for _, word := range strings.Fields(body) {
			word = strings.Trim(word, "\"'.,!?()[]{}")
			if word == "" || seen[word] {
				continue
			}
			seen[word] = true
			words = append(words, word)
			if len(words) >= 12 {
				break
			}
		}
		if len(words) >= 12 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"", "   ", "!!!", "???",
		"no-such-word-anywhere",
		"word word",          // repeated token
		`"quoted phrase"`,    // quotes fold to spaces, then re-quoted
		`say "hello" twice"`, // stray quote becomes a doubled quote token
		"a-b",                // hyphen folds to a space: two tokens
		"UPPER Case",         // porter stemmer folds case
	}
	queries = append(queries, words...)
	if len(words) >= 2 {
		queries = append(queries, words[0]+" "+words[1], words[0]+" "+words[len(words)-1])
	}
	return queries
}

// TestSearchQueryMirror holds the fastdb word fold byte-equal to
// database.SearchQuery over a broad deterministic corpus, including unicode
// letters, marks and connector punctuation.
func TestSearchQueryMirror(t *testing.T) {
	corpus := []string{
		"", "plain words", "  spaced  out  ",
		"punctuation,;:.!?()[]{}<>\"'*^%$#@&~`|\\/",
		"café naïve résumé",            // combining vs precomposed accents
		"e\u0301tude",                  // combining mark
		"under_score dash-token a+b=c", // Pc kept, hyphen/plus folded
		"--double--", "a\tb\r\nc",      // control characters fold
		"123 digits 456", "中文 漢字", "emoji 🚀🔥",
		"a\u00a0b", // non-breaking space folds (not letters/numbers/marks/Pc)
		"١٢٣",      // arabic-indic digits are numbers
	}
	for _, in := range corpus {
		want := database.SearchQuery(in)
		got := searchQuery(in)
		if got != want {
			t.Errorf("searchQuery(%q) = %q, database.SearchQuery = %q", in, got, want)
		}
	}
	// Every input's output must be fold-idempotent like the database's.
	for _, in := range corpus {
		if once := searchQuery(in); searchQuery(once) != once {
			t.Errorf("searchQuery(%q) not idempotent: %q", in, once)
		}
	}
}

// TestSearchWordFoldReset pins the fold against the unicode classes it is
// built from: a rune keeps through exactly when database.SearchQuery keeps
// it, which is the fragment of unicode the two implementations must agree on.
func TestSearchWordFoldReset(t *testing.T) {
	samples := []rune{'a', 'Z', '0', '9', '_', '‿', '⁀', '︳', '﹍', '・', 'é', '\u0301', '中', ' ', '-', '"', '\t', '🚀', '١'}
	for _, r := range samples {
		want := database.SearchQuery(string(r))
		got := searchQuery(string(r))
		if got != want {
			t.Errorf("rune %U: searchQuery %q != database.SearchQuery %q", r, got, want)
		}
	}
}
