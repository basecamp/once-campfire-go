package presentation

import (
	"sync"
	"testing"
)

func TestSharedHTMLEscapingPreservesBytes(t *testing.T) {
	const input = "\"'&<> +\x00日本語\u2028\u2029\xff &amp;"
	const want = "&quot;&#39;&amp;&lt;&gt; +\x00日本語\u2028\u2029\xff &amp;amp;"
	var work sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		work.Go(func() {
			<-start
			if got := escape(input); got != want {
				t.Errorf("escaping changed: %q, want %q", got, want)
			}
		})
	}
	close(start)
	work.Wait()
}
