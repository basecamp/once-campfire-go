# Host activity and earlier attempts

The initial three-way pass was stopped during repetition 2 after concurrent VM
compilation activity. Its whole attempt is excluded from acceptance, including
completed samples. Unedited raw samples and logs remain in
[`final-contended/`](https://github.com/nick-potts/once-campfire-go/tree/32c6f7507c75c629e7f0f642a67637e90abeb0e0/bench/results/rust-parity-20261005/final-contended). Optimized Go history fell to 1,790
requests/sec and writes to 574/sec; write p99 reached 1,141 ms.

The complete exploratory passes in `step12-priority`, `step13-priority` and
`step14-priority` failed the target and are retained. They preceded the final
changes to observers, checkpoint cadence, response framing or reaction forms.
No completed sample from the final pass was removed or selectively replaced.
The final application binaries were fixed before its first measurement.

The harness and its children inherit nice -10 to reduce CPU interference. Both
applications use identical priority, affinity, worker counts, seed data, response
checks, warmup and duration. The final pass compares current Go and Rust in
alternating order; earlier three-way runs also measured the unchanged Go baseline.

Host load and source/binary hashes are retained in each metadata file. Scheduling
priority does not eliminate host I/O or memory contention. These are measurements
on a shared development VM, not dedicated bare-metal results. Write ranges overlap;
the narrow median write advantage is not evidence of a statistically robust win.
