# Host contention and repeated comparison

The first three-way pass was stopped during repetition 2 after severe concurrent
VM compilation activity. The whole attempt is excluded from acceptance, including
its completed samples. Its unedited raw samples and logs remain in
[`../final-contended/`](../final-contended/). Optimized Go history dropped to 1,790
requests/sec and writes to 574/sec; the write p99 reached 1,141 ms. Earlier trials
measured roughly 9,200 history requests/sec and 3,200 writes/sec. Host load and
source/binary hashes are retained in that attempt's metadata.

The comparison was restarted from repetition 1 with unchanged application binaries,
raising the harness scheduling priority to nice -10. Original Go, optimized Go,
Rust, and the load generator all inherit the same priority. CPU affinity, worker
counts, seed data, response checks, warmup, duration, and rotation are unchanged.
Background processes are still present; scheduling priority reduces CPU interference
but does not eliminate host I/O or memory contention. This is a local benchmark,
not a dedicated bare-metal result. All completed samples from the restarted pass
are retained.
