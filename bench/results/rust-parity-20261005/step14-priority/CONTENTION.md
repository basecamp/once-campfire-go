# Host activity and earlier attempts

The initial three-way pass was stopped during repetition 2 after severe concurrent
VM compilation activity. Its whole attempt is excluded from acceptance, including
completed samples. Unedited raw samples and logs remain in
[`../final-contended/`](../final-contended/). Optimized Go history fell to 1,790
requests/sec and writes to 574/sec; write p99 reached 1,141 ms.

[`../step12-priority/`](../step12-priority/) and
[`../step13-priority/`](../step13-priority/) are complete three-way exploratory
comparisons. They failed the throughput target and are retained in full. The
subsequent observer-pool, checkpoint-cadence, and HTTP framing changes have
separate correctness coverage; no completed sample from the final pass is
removed or selectively replaced.

For these runs and the final pass, the harness and all children inherit nice -10
to reduce CPU interference. Go and Rust use identical priority, CPU affinity,
worker counts, seed data, response checks, warmup and duration. The final pass
compares the two current applications in alternating order, rather than spending
a third of measurement time on the previously recorded baseline.

Host load and source/binary hashes are retained in each metadata file. Scheduling
priority does not eliminate host I/O or memory contention. These are measurements
on a shared development VM, not dedicated bare-metal results.
