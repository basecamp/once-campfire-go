# Single session read per request: before/after, 2026-10-09

Relative before/after measurement of `single-session-activity-query` against its base `9b0ef06` with the unmodified [once-campfire-verification](https://github.com/basecamp/once-campfire-verification) harness at `e244051`. These are not comparable with the published Linux table: see Limitations.

**Result: flat.** Authentication reads the session once instead of twice, which removes exactly one read-pool query from every authenticated request (measured below), but throughput differences are within this environment's noise on all routes in both profiles. No throughput gain is claimed. Peak memory is unchanged.

## What was run

Each block is one unmodified `bin/benchmark` call with two rounds. Blocks alternate `main, A, A, main`, three times: twice for the normal profile and once for the mixed-write profile. Each side has 8 normal samples and 4 mixed samples per route.

```sh
GO_IMAGE=campfire-go-bench:<main|a> bin/benchmark --apps go --rounds 2 --duration 8 --concurrencies 16 \
  --cpus 2-5 --client-cpus 6-9 --routes room_show,messages_page,sidebar,search,post_message \
  --output tmp/bench/results/<block>

GO_IMAGE=campfire-go-bench:<main|a> bin/benchmark --apps go --rounds 2 --duration 8 --concurrencies 16 \
  --cpus 2-5 --client-cpus 6-9 --routes room_show,messages_page,sidebar,search --mixed-write-rate 10 \
  --output tmp/bench/results/<block>
```

Harness defaults kept: 16 clients, gzip, 2 s warmup at 4 clients, 8 s timed samples, fail-closed route-contract validation (`route-contract-v1`), exact persisted-write and FTS audits, databases and files on tmpfs, 64 MB response cache. The harness was run as uid 1000, as the app image's user. The exact orchestration scripts (`harness/run-bench.sh`, `run-bench2.sh`, `pass2.sh`) and the Linux host's Dockerfile are in the raw archive (see Raw data).

| Block | Image | Mac 1-min load before | VM 1-min load before | Start |
|---|---|---:|---:|---|
| 01-normal-main | main | 12.02 | 8.08 | 11:10:06 |
| 02-normal-a | A | 16.03 | 8.31 | 11:12:01 |
| 03-normal-a | A | 21.20 | 8.45 | 11:13:53 |
| 04-normal-main | main | 21.91 | 10.64 | 11:15:50 |
| 09-mixed-main | main | 23.70 | 9.63 | 11:25:05 |
| 10-mixed-a | A | 13.77 | 11.03 | 11:26:37 |
| 11-mixed-a | A | 11.75 | 9.90 | 11:28:08 |
| 12-mixed-main | main | 18.69 | 8.90 | 11:29:39 |
| 17-normal-main | main | 8.63 | 9.34 | 11:37:21 |
| 18-normal-a | A | 8.14 | 9.96 | 11:39:11 |
| 19-normal-a | A | 10.76 | 9.10 | 11:41:03 |
| 20-normal-main | main | 8.85 | 8.17 | 11:42:55 |

The Mac has 10 cores. The B comparison (`sqlite-memstatus-off`) ran interleaved in the same session as blocks 05–08, 13–16 and 21–24 with its own `main` baselines; it is reported in `../20261009-sqlite-memstatus-off/`. Both changes now ship together; each was measured alone against `main`, so these two folders give the per-change attribution.

## Results

Requests/sec at 16 clients, median (min–max) per route.

Normal profile, 8 samples per side (blocks 01–04 and 17–20):

| Route | main | A | Change |
|---|---:|---:|---:|
| Room page | 22,132 (20,168–25,508) | 22,781 (5,492–26,170) | +2.9% |
| Messages page | 23,322 (21,887–26,166) | 25,157 (3,481–27,131) | +7.9% |
| Sidebar | 26,070 (22,312–28,996) | 25,766 (14,142–28,622) | -1.2% |
| Search | 25,428 (6,899–28,309) | 24,759 (8,923–27,929) | -2.6% |
| Post a message | 3,511 (1,103–3,749) | 3,125 (1,935–3,753) | -11.0% |

The second, quieter pass alone (blocks 17–20, Mac load 8.1–10.8) is also flat: room +4.7%, messages +4.7%, sidebar -0.3%, search -0.6%, posting +0.4%, with overlapping ranges.

Mixed-write profile, 4 samples per side (blocks 09–12), one writer capped at 10 posts/sec to HQ:

| Route | main | A | Change |
|---|---:|---:|---:|
| Room page | 17,384 (11,193–22,349) | 18,978 (9,355–23,820) | +9.2% |
| Messages page | 19,121 (4,180–22,073) | 23,939 (3,942–24,492) | +25.2% |
| Sidebar | 28,536 (18,280–29,518) | 30,649 (24,850–31,284) | +7.4% |
| Search | 23,578 (10,271–28,410) | 28,822 (26,756–30,342) | +22.2% |

The mixed ranges overlap on all four routes and baseline block 09 ran at a Mac load of 23.7; these medians are not evidence of a gain. Writers acknowledged 1,260 timed writes per side at 9.6–9.9 writes/sec.

Validation: 6,352,610 (main) and 5,816,624 (A) timed normal responses, 2,588,918 and 3,037,817 timed mixed reads, all with zero errors and zero invalid responses; every acknowledged write matched its database, rich-text and FTS rows. Peak generator CPU was 129–135% of the 400% available on its four CPUs.

## Peak memory

Sampled every 0.5 s inside the Linux host for the whole round (startup, login, contract preparation, warmups and all routes): the server process's peak RSS (`VmHWM`) and the container cgroup's `memory.peak`. Median (min–max) of the per-round peaks:

| Profile | Metric | main | A |
|---|---|---:|---:|
| Normal (8 rounds each) | Server peak RSS | 134.3 MiB (129.3–135.0) | 133.5 MiB (131.6–138.0) |
| Normal (8 rounds each) | cgroup memory.peak | 135.3 MiB (127.1–137.6) | 136.3 MiB (128.5–145.7) |
| Mixed (4 rounds each) | Server peak RSS | 136.6 MiB (135.8–151.8) | 134.7 MiB (131.9–149.2) |
| Mixed (4 rounds each) | cgroup memory.peak | 134.0 MiB (126.1–144.7) | 132.9 MiB (126.0–141.3) |

One sampler line was taken after a process had exited; its empty cgroup path read the root cgroup's `memory.peak` (2,648 MiB). The analysis excludes lines without a live process.

## Query count

There is no query hook in the code, so this was measured with temporary instrumentation that was not kept: an atomic counter in `readPool.QueryContext`/`QueryRowContext` and a throwaway test making the same request twice on a fresh test app. Read-pool queries per request (the `PRAGMA data_version` checks run on a separate connection and are not counted):

| Request | main, cache hit | A, cache hit | main, cache off | A, cache off |
|---|---:|---:|---:|---:|
| Room page | 3 | 2 | 6 | 5 |
| Messages page | 4 | 3 | 4 | 3 |
| Sidebar | 2 | 1 | 6 | 5 |
| Search | 2 | 1 | 6 | 5 |

## Images and sources

Both images were built with `docker build --platform linux/arm64` from the repository's own Dockerfile, with `APP_VERSION=bench-<tag>` and `GIT_REVISION=9b0ef062ea9732c6f611b705d77b52aeec31301f`.

| Image | Source | Image ID in Docker Desktop | Image ID in the bench host | `/usr/local/bin/campfire` sha256 |
|---|---|---|---|---|
| main | `9b0ef06`, clean | `sha256:210c69b5546a8e01c955fdb6752c8e2d38aa848381156722187be02111fbc36c` | `sha256:21efd64e14aa3dd753915c82e14cf7d8ae71ee14f01ee3cccd3b56c34924bd5d` | `4d20ecbd77d71ba6acef7bde9781cb6ff6fd8da9403bfb7fcbcc9eb54789582d` |
| A | `9b0ef06` plus only the session-read change (measured before the two changes were combined) | `sha256:3ee8b84c6116f5c7ed376e4def4bc921ac2ad5acaab6dab5d4818f46c089a2b0` | `sha256:47e6afe494015561ad41774aab16ab21a57a7896c615554e98286a0ee668ad3f` | `c6727f38ea53846b6cb79f1eb3aab6cff7fa871dd2fd07942766af60268cf4ed` |

Docker Desktop's image store and the bench host's classic store report different IDs for the same `docker save`/`docker load` image, so the binary hash was compared inside both; it matches. The harness metadata records the bench-host IDs and `source_revisions.go.dirty: true` for A, because the change was measured uncommitted.

Seed: built by the harness's `bin/seed` (Rails `90b3300`, generated locally with fresh disposable credentials), SHA-256 `8dcc238c1888e74f87b78b15ee3e647661b05734e6cb5955558f0a1822f5b25c` (`logs/seed-source.json` in the raw archive). Loadgen: built from the harness's `loadgen/` with `rust:1-alpine` for linux/arm64 musl, SHA-256 `9d72dddf12dd8362c4f01036768dfed5d44120416f61eade123262b6301e61fc`. Both differ from the published report's hashes because they were built here.

## Limitations

- Docker Desktop 29.1.3 on an Apple M1 Max (arm64), Linux VM with 10 vCPUs and 8 GB. The harness and its loadgen ran unmodified inside a privileged `docker:29-dind` container in that VM, which gives it `taskset`, `/proc/loadavg`, host networking and tmpfs; the app containers ran in that inner Docker.
- VM CPUs were pinned (server 2–5, generator 6–9, memory sampler 0–1), but the VM's vCPUs share the Mac's cores with everything else on the Mac. The host was not idle: other sessions ran Go builds and tests (Mac load 8–24 during the blocks), and the shared ClickHouse/HyperDX/Hatchet/Redis/Mailpit/RustFS containers ran in the same VM throughout (VM load 8–11 before each block). Single samples dropped by up to 85% when the Mac was busy, which is why the ranges are wide.
- Relative before/after only, within one session. The absolute numbers are not comparable with the published table (AMD, native Linux, CPUs 8–11) and must not be presented as such.
- One viewer session per round, warm caches; cold reads, multiple viewers, Action Cable, TLS and durability on real disks were not measured.
- Each block runs two rounds back to back, so baseline and candidate are interleaved by block rather than by round.

## Failed attempts, kept

- The first block failed to start: the harness ran as root inside the bench host, so the databases it copied were read-only to the app's uid 1000 (`attempt to write a readonly database`). Fixed by running the harness as uid 1000; no sample was taken.
- The next attempt completed round 1 of block 01 and then stopped with `go: source changed between rounds` (the harness's own source-identity guard). The cause was not identified; a later check of the same mount showed a stable HEAD and clean status. The run was restarted from block 01; that attempt's log is in `logs/failed-attempt/` in the raw archive, and none of its samples are used.

## Files

- `samples.tsv`: every timed sample, one row each: change, block, image (`main` or `candidate`), profile, round, route, clients, req/s, validated responses, invalid responses, errors, Mac 1-minute load before the block, VM 1-minute load at round start, the round's peak server RSS and cgroup `memory.peak`, and for the mixed profile the writer's acknowledged writes and rate. Its medians, ranges, totals and peak-memory figures reproduce the tables above exactly.
- `analysis.md`: the summary tables computed from the raw blocks.

## Raw data

The full harness output (per-block `summary.json`, `go-*.json`, `raw/http-*.json`), console logs, memory samples, image identities, the failed attempt's log, and the orchestration and analysis scripts for both changes in this PR: [campfire-go-pr11-bench-raw-20261009.tar.gz](https://github.com/khanakia/once-campfire-go/releases/download/pr11-bench-raw-20261009/campfire-go-pr11-bench-raw-20261009.tar.gz), sha256 `cb0f8e34a4d73cfd2c64d7796707528bff33031574610ad09e151d56effb7ca8`.
