# Interrupted repetition and resumption

The first Rust/Go pair completed under normal host load (1-minute load at the
end: Rust 5.19, Go 3.56). During Go repetition 2, a separate C++ sanitizer build
and clang-tidy run began. Host load exceeded 20 and throughput fell sharply.
That repetition was interrupted and its incomplete samples excluded. Another build
interrupted Rust repetition 2 after Go repetition 2 had completed. The other
build was not modified or stopped.

The runner supports resumption only when binaries, sources, seed, dependencies,
load generator and benchmark settings match. Completed first-pair samples were
retained; the interrupted repetition is rerun in full. Metadata records each
resumption. The interrupted Go-2 logs are retained with an interrupted prefix.

For the remaining runs, a monitor polls external compiler processes each second.
Initially any compiler triggered a restart; after repeated interruptions by isolated
single-file compilations, the threshold was narrowed to four concurrent compiler
processes. It waits for 20 seconds without that parallel compilation before
resuming, and interrupts the benchmark if parallel compilation returns. Every interrupted application repetition is
retried from a fresh seed; completed repetitions are retained. Automatic events
are recorded in `contention-events.json`. This detects the observed interference,
not every possible source of workstation noise. Ordinary background services remain.
