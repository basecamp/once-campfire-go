package database

import (
	"testing"
	"time"
)

func TestBindsTimesAsTheReferencesToDB(t *testing.T) {
	for _, c := range []struct {
		time time.Time
		want string
	}{
		{time.Date(2026, 3, 2, 16, 0, 0, 0, time.UTC), "2026-03-02 16:00:00"},
		{time.Date(2026, 3, 2, 16, 0, 0, 123000, time.UTC), "2026-03-02 16:00:00.000123"},
		{time.Date(2026, 3, 2, 17, 0, 0, 999, time.FixedZone("CET", 3600)), "2026-03-02 16:00:00"},
	} {
		if got := ToDB(c.time); got != c.want {
			t.Errorf("ToDB(%v) = %q, want %q", c.time, got, c.want)
		}
	}
}
