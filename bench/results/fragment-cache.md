# Message-fragment cache measurement

Development measurement on 2026-10-02. One repetition, two-second samples after warmup; this is a directional before/after check, not the final Go/Rust comparison. Both snapshots predate later HTTP/template compatibility fixes.

The cache stores fully rendered message fragments with invalidation tied to their data, so room history and search can reuse presentation work. Raw responses, source hashes and settings remain in the linked directories.

| Workload | Clients | Before req/s | After req/s | Ratio |
|---|---:|---:|---:|---:|
| room_show | 1 | 57 | 1,211 | 21.32× |
| room_show | 16 | 214 | 3,278 | 15.30× |
| messages_page | 1 | 62 | 2,022 | 32.72× |
| messages_page | 16 | 230 | 5,261 | 22.89× |
| search | 1 | 178 | 3,044 | 17.14× |
| search | 16 | 643 | 9,097 | 14.15× |

[Before raw samples](application-baseline-complete-20261002/raw.json); [after raw samples](application-fragment-cache-20261002/raw.json).

The old upload selector fetched the message avatar rather than its attachment thumbnail; media timings in these development runs are invalid. The final application runner fetches a representation URL and verifies thumbnail hashes.
