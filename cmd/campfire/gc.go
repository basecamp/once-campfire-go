package main

import (
	"errors"
	"log/slog"
	"math"
	"runtime/debug"
	"strconv"
	"strings"
)

// applyGCPolicy applies the runtime GC knobs before the server starts.
//
// CAMPFIRE_GOGC takes the GOGC target percentage (any integer; "off", like
// GOGC=off, maps to -1 and disables the collector). CAMPFIRE_GOMEMLIMIT takes
// a soft memory limit in bytes, optionally with a MiB or GiB suffix. Reading
// them through runtime/debug instead of the runtime's own GOGC/GOMEMLIMIT
// environment handling keeps the knobs explicit: an absent knob leaves the
// runtime's setting untouched (Go already reads both variables itself), and
// an invalid value logs a warning and keeps it.
//
// The returned values are the effective settings the process runs with, read
// back from the runtime for the startup log line: SetMemoryLimit(-1) reads
// without adjusting, and SetGCPercent has no read-only form, so its current
// value is captured by setting a sentinel and restoring it. Both accesses are
// startup-only and single-threaded.
func applyGCPolicy(lookup func(string) (string, bool)) (gcPercent int, memLimit int64) {
	if value, ok := lookup("CAMPFIRE_GOGC"); ok {
		if percent, err := parseGCPercent(value); err == nil {
			debug.SetGCPercent(percent)
		} else {
			slog.Warn("gc: ignoring CAMPFIRE_GOGC, keeping the runtime setting", "value", value, "error", err)
		}
	}
	if value, ok := lookup("CAMPFIRE_GOMEMLIMIT"); ok {
		if limit, err := parseMemLimit(value); err == nil {
			debug.SetMemoryLimit(limit)
		} else {
			slog.Warn("gc: ignoring CAMPFIRE_GOMEMLIMIT, keeping the runtime setting", "value", value, "error", err)
		}
	}
	percent := debug.SetGCPercent(100)
	debug.SetGCPercent(percent)
	limit := debug.SetMemoryLimit(-1)
	slog.Info("gc policy", "gc_percent", percent, "mem_limit", limit, "mem_limit_unit", "bytes")
	return percent, limit
}

// parseGCPercent parses a CAMPFIRE_GOGC value. The runtime's own GOGC syntax
// applies: an integer target percentage, or "off" (lowercase, as GOGC spells
// it) meaning -1; any negative value disables the collector.
func parseGCPercent(value string) (int, error) {
	if value == "off" {
		return -1, nil
	}
	percent, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New(`expected an integer percentage or "off"`)
	}
	return percent, nil
}

// parseMemLimit parses a CAMPFIRE_GOMEMLIMIT value: a byte count (for
// example "536870912") or a count with a MiB or GiB suffix ("512MiB",
// "2GiB"), matching the binary IEC units of GOMEMLIMIT's own syntax.
// Negative values are rejected: the runtime treats a negative input as a
// read of the current limit, not as a setting, so "no limit" can only be
// expressed by omitting the knob.
func parseMemLimit(value string) (int64, error) {
	unit := int64(1)
	switch {
	case strings.HasSuffix(value, "MiB"):
		value, unit = value[:len(value)-3], 1<<20
	case strings.HasSuffix(value, "GiB"):
		value, unit = value[:len(value)-3], 1<<30
	}
	bytes, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, errors.New("expected a byte count, optionally with a MiB or GiB suffix")
	}
	if bytes < 0 {
		return 0, errors.New("negative byte count")
	}
	if unit > 1 && bytes > math.MaxInt64/unit {
		return 0, errors.New("value overflows a byte count")
	}
	return bytes * unit, nil
}
