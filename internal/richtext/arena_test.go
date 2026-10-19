package richtext

// ENGINE-56 steady-state allocation assertions: after the freelist has
// warmed, processing a typical message body must allocate only the output
// buffers (the Result's strings and mention slice) — everything else —
// parse trees, serializations, plain text, presentation — lives in the
// reused arena. The bounds below are deliberately generous (outputs vary
// with body size) but far below the per-node allocation count the previous
// implementation needed.

import (
	"strings"
	"testing"

	xhtml "github.com/basecamp/once-campfire-go/internal/html"
)

func TestProcessMessageSteadyStateAllocs(t *testing.T) {
	type tc struct {
		name, body string
		max        int
	}
	cases := []tc{
		{"plain", "First post!", 8},
		{"script", "if (a < b) { run(); }", 8},
		{"markup", "<div><p>hello <b>bold</b> and <i>italic</i></p><p>second line</p></div>", 8},
		{"entities", "<p>1 &lt; 2 &amp;&amp; 3 &gt; 2 \"quoted\" 'single'</p>", 9},
		{"list", "<ul><li>one</li><li>two</li><li>three</li></ul>", 9},
		{"emoji", "<p>😄🤘 emoji body</p>", 9},
		{"smiley", "<p>😊 smiley 😎 cool</p>", 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var result Result
			warmup(t, c.body, &result)
			n := testing.AllocsPerRun(200, func() {
				r, err := ProcessMessage(c.body, Context{})
				if err != nil {
					t.Fatal(err)
				}
				sink = r
			})
			t.Logf("%s: %d allocs/op", c.name, int(n))
			if int(n) > c.max {
				t.Fatalf("%s: %d allocs/op, want <= %d (result %+v)", c.name, int(n), c.max, sink)
			}
			if sink.Plain == "" && c.body != "if (a < b) { run(); }" {
				t.Fatalf("%s: output lost", c.name)
			}
			if !strings.Contains(sink.Presentation, "lexxy-content") {
				t.Fatalf("%s: presentation lost: %q", c.name, sink.Presentation)
			}
		})
	}
}

// sink defeats dead-code elimination of benchmark/test outputs.
var sink Result

func warmup(t *testing.T, body string, result *Result) {
	t.Helper()
	for i := 0; i < 5; i++ {
		r, err := ProcessMessage(body, Context{})
		if err != nil {
			t.Fatal(err)
		}
		*result = r
	}
}

// TestArenaViewLifetime pins the freelist contract: Result values are real
// strings that survive the arena's reuse. The pipeline is driven directly on
// one arena so reuse is deterministic, and the second fill must not corrupt
// the first call's outputs — the poisoning case sync.Pool-style reuse
// forbids.
func TestArenaViewLifetime(t *testing.T) {
	a := xhtml.NewArena()
	defer a.Reset()
	first, err := processMessage(a, "<p>alpha <b>beta</b></p>", Context{})
	if err != nil {
		t.Fatal(err)
	}
	p := first.Presentation
	pl := first.Plain
	a.Reset()
	second, err := processMessage(a, "<p>gamma <i>delta</i> &amp; omega</p>", Context{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Presentation != p || first.Plain != pl {
		t.Fatal("outputs changed after the arena was reused: result fields alias the arena")
	}
	if second.Presentation == first.Presentation {
		t.Fatal("distinct calls must not share backing memory")
	}
	if !strings.Contains(second.Presentation, "gamma") || !strings.Contains(second.Plain, "gamma") {
		t.Fatalf("second call outputs corrupted: %q %q", second.Presentation, second.Plain)
	}
}

// TestFreelistConcurrent exercises the freelist under -race: concurrent
// messages must not share arenas.
func TestFreelistConcurrent(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				r, err := ProcessMessage("<p>concurrent <b>body</b> &amp; more</p>", Context{})
				if err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(r.Presentation, "concurrent") {
					t.Error("corrupted presentation under concurrency")
					return
				}
			}
		}()
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}
