# Go optimization results

The full port was optimized without adding a web framework or changing its storage and signing contracts. These are application measurements on this workstation, not a language-wide comparison.

## HTTP throughput

Median requests/sec at 16 clients. Both reports use three repetitions, five-second samples, identical seed data, identity encoding and four application CPUs. The original Go measurements are from the earlier baseline report; the optimized Go and Rust measurements are from the new alternating comparison.

| Workload | Original Go | Optimized Go | Go improvement | Rust | Rust / optimized Go |
|---|---:|---:|---:|---:|---:|
| Room page | 2,483 | 10,629 | 4.28× | 27,900 | 2.62× |
| Message history | 4,470 | 21,582 | 4.83× | 31,622 | 1.47× |
| Sidebar | 1,425 | 26,531 | 18.62× | 38,578 | 1.45× |
| Search | 6,652 | 15,285 | 2.30× | 30,698 | 2.01× |
| Avatar | 10,195 | 40,710 | 3.99× | 57,148 | 1.40× |
| Static CSS | 302,343 | 314,163 | 1.04× | 498,442 | 1.59× |
| Post message | 3,180 | 4,068 | 1.28× | 7,849 | 1.93× |

## Broadcast throughput

Median complete broadcasts/sec; each broadcast must reach every connected client.

| Clients | Compression | Original Go | Optimized Go | Go improvement | Rust |
|---:|---|---:|---:|---:|---:|
| 100 | none | 575.9 | 1,665.2 | 2.89× | 4,105.4 |
| 100 | deflate | 454.8 | 1,877.9 | 4.13× | 3,461.7 |
| 1,000 | none | 54.2 | 236.7 | 4.37× | 566.6 |
| 1,000 | deflate | 44.2 | 289.6 | 6.55× | 373.3 |
| 10,000 | none | 4.1 | 17.8 | 4.34× | 67.8 |
| 10,000 | deflate | 3.5 | 21.1 | 6.03× | 39.3 |

## Interpretation and verification

Rust remains faster. Repeated PBKDF2 derivation, redundant SQLite preparation, HTML copying/rendering and per-client broadcast compression explained much of the original gap. The changes and exploratory profiles are described in [the investigation record](README.md). Go still pays for runtime templates, database/sql/CGO work, allocation and per-connection scheduling. These profiles do not isolate a universal language cost.

The optimized binary passed formatting, vet, race tests, rich-text vectors, browser workflows, cookie/message interoperability and production-container backup/restore checks. A fresh default-seed desktop inventory matched all 178 applicable screenshots and accessibility trees; eight phone cells also matched. Previously documented strict DOM/network and deleted-room typing differences remain. See [validation](../../../plans/validation.md#optimization-verification--2026-10-03).

See the [full optimized benchmark](../application-optimized-20261003/report.md) for concurrency 1/16/64, latency, bytes, memory and startup; [raw samples](../application-optimized-20261003/raw.json), [metadata](../application-optimized-20261003/metadata.json), and [original baseline](../application-final-20261002/report.md).

External C++ builds interrupted some repetitions. Those incomplete repetitions were excluded and restarted from fresh seeds with unchanged binaries/settings; completed quiet runs were retained. The [interruption record](../application-optimized-20261003/CONTENTION.md) explains detection and remaining workstation-noise limitations. All completed samples, including slower valid ones, contribute to the medians.
