package front

import (
	"context"
	"os"
	"testing"

	"github.com/basecamp/once-campfire-go/internal/responsebody"
)

func BenchmarkPreparedGzip(b *testing.B) {
	body, err := os.ReadFile(os.Getenv("CAMPFIRE_BENCH_BODY"))
	if err != nil {
		b.Fatal("set CAMPFIRE_BENCH_BODY to a captured full identity response:", err)
	}
	parts := []responsebody.Part{responsebody.NewPart(body)}
	for _, phase := range []string{"warm", "prepare", "cold", "compress"} {
		b.Run(phase, func(b *testing.B) {
			cache := newGzipCache(gzipCacheBytes)
			encoded, err := cache.prepare(context.Background(), parts, 0)
			if err != nil {
				b.Fatal(err)
			}
			if phase != "warm" {
				b.SetBytes(int64(len(body)))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if phase == "warm" {
					_, err = cache.prepare(context.Background(), parts, 0)
				} else if phase == "prepare" {
					_, err = cache.prepare(context.Background(), []responsebody.Part{responsebody.NewPart(body)}, 0)
				} else if phase == "cold" {
					_, err = newGzipCache(gzipCacheBytes).prepare(context.Background(), []responsebody.Part{responsebody.NewPart(body)}, 0)
				} else {
					_, err = gzipBody(parts, 0)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(encoded)), "gzip-bytes")
		})
	}
}
