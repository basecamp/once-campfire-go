package main

import (
	"bytes"
	"log/slog"
	"runtime/debug"
	"strings"
	"testing"
)

// mapLookup turns a literal env snapshot into the lookup form the GC policy
// reads, so tests never touch the real environment.
func mapLookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestParseGCPercent(t *testing.T) {
	tests := []struct {
		value string
		want  int
		ok    bool
	}{
		{"200", 200, true},
		{"0", 0, true},
		{"-5", -5, true}, // any negative value disables the collector
		{"off", -1, true},
		{"", 0, false},
		{"OFF", 0, false}, // the runtime's own GOGC syntax is lowercase "off"
		{"two hundred", 0, false},
		{"1.5", 0, false},
		{" 200", 0, false},
	}
	for _, test := range tests {
		got, err := parseGCPercent(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parseGCPercent(%q) = %d, %v; want %d, ok=%v", test.value, got, err, test.want, test.ok)
		}
	}
}

func TestParseMemLimit(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{"536870912", 512 << 20, true},
		{"0", 0, true},
		{"512MiB", 512 << 20, true},
		{"2GiB", 2 << 30, true},
		{"512mib", 0, false}, // suffix case matches GOMEMLIMIT's own syntax
		{"512 KiB", 0, false},
		{"", 0, false},
		{"1.5GiB", 0, false},
		{"-1", 0, false},                     // the runtime reads, not sets, on negative input
		{"9223372036854775807GiB", 0, false}, // overflows int64 bytes
	}
	for _, test := range tests {
		got, err := parseMemLimit(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parseMemLimit(%q) = %d, %v; want %d, ok=%v", test.value, got, err, test.want, test.ok)
		}
	}
}

func TestApplyGCPolicy(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		basePercent int
		baseLimit   int64
		wantPercent int
		wantLimit   int64
	}{
		{"percent and limit applied", map[string]string{"CAMPFIRE_GOGC": "200", "CAMPFIRE_GOMEMLIMIT": "512MiB"}, 100, 1 << 40, 200, 512 << 20},
		{"off disables the collector", map[string]string{"CAMPFIRE_GOGC": "off", "CAMPFIRE_GOMEMLIMIT": "2GiB"}, 100, 1 << 40, -1, 2 << 30},
		{"absent knobs keep the runtime settings", nil, 50, 2 << 30, 50, 2 << 30},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The knobs are process-global; pin a baseline and restore the
			// true previous settings when the subtest is done.
			prevPercent := debug.SetGCPercent(test.basePercent)
			prevLimit := debug.SetMemoryLimit(test.baseLimit)
			t.Cleanup(func() {
				debug.SetGCPercent(prevPercent)
				debug.SetMemoryLimit(prevLimit)
			})

			percent, limit := applyGCPolicy(mapLookup(test.env))
			if percent != test.wantPercent {
				t.Fatalf("reported gc percent = %d, want %d", percent, test.wantPercent)
			}
			if limit != test.wantLimit {
				t.Fatalf("reported mem limit = %d, want %d", limit, test.wantLimit)
			}
			// Round trip: each setter returns the previously applied value,
			// so the policy's values are observable in the runtime itself.
			if got := debug.SetGCPercent(test.basePercent); got != test.wantPercent {
				t.Fatalf("effective gc percent = %d, want %d", got, test.wantPercent)
			}
			if got := debug.SetMemoryLimit(test.baseLimit); got != test.wantLimit {
				t.Fatalf("effective mem limit = %d, want %d", got, test.wantLimit)
			}
		})
	}
}

func TestApplyGCPolicyInvalidKeepsRuntimeSettings(t *testing.T) {
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	prevPercent := debug.SetGCPercent(75)
	prevLimit := debug.SetMemoryLimit(1 << 30)
	t.Cleanup(func() {
		debug.SetGCPercent(prevPercent)
		debug.SetMemoryLimit(prevLimit)
	})

	percent, limit := applyGCPolicy(mapLookup(map[string]string{
		"CAMPFIRE_GOGC":       "two hundred",
		"CAMPFIRE_GOMEMLIMIT": "512KiB",
	}))
	if percent != 75 {
		t.Fatalf("gc percent = %d, want unchanged 75", percent)
	}
	if limit != 1<<30 {
		t.Fatalf("mem limit = %d, want unchanged %d", limit, int64(1)<<30)
	}
	warnings := buffer.String()
	if !strings.Contains(warnings, "CAMPFIRE_GOGC") {
		t.Fatalf("no warning for CAMPFIRE_GOGC; log:\n%s", warnings)
	}
	if !strings.Contains(warnings, "CAMPFIRE_GOMEMLIMIT") {
		t.Fatalf("no warning for CAMPFIRE_GOMEMLIMIT; log:\n%s", warnings)
	}
}
