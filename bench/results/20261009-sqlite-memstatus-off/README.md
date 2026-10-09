# SQLite without memory statistics: before/after, 2026-10-09

Relative before/after measurement of `sqlite-memstatus-off` against its base `9b0ef06` with the unmodified [once-campfire-verification](https://github.com/basecamp/once-campfire-verification) harness at `e244051`. These are not comparable with the published Linux table: see Limitations.

**Result: a small gain, mostly within this environment's noise.** The candidate's median is higher on all nine route/profile pairs. The clearest gains are the room page (+11% normal, +12% in the quieter second pass, +19% mixed) and posting (+6%). Messages, sidebar and search moved +1–5% in the normal profile, inside the baseline's range. The candidate blocks also happened to run at a somewhat lower Mac load than some baseline blocks (see the block table), which may account for part of the difference. Peak memory is unchanged.

## What was run

Each block is one unmodified `bin/benchmark` call with two rounds. Blocks alternate `main, B, B, main`, three times: twice for the normal profile and once for the mixed-write profile. Each side has 8 normal samples and 4 mixed samples per route.

```sh
GO_IMAGE=campfire-go-bench:<main|b> bin/benchmark --apps go --rounds 2 --duration 8 --concurrencies 16 \
  --cpus 2-5 --client-cpus 6-9 --routes room_show,messages_page,sidebar,search,post_message \
  --output tmp/bench/results/<block>

GO_IMAGE=campfire-go-bench:<main|b> bin/benchmark --apps go --rounds 2 --duration 8 --concurrencies 16 \
  --cpus 2-5 --client-cpus 6-9 --routes room_show,messages_page,sidebar,search --mixed-write-rate 10 \
  --output tmp/bench/results/<block>
```

Harness defaults kept: 16 clients, gzip, 2 s warmup at 4 clients, 8 s timed samples, fail-closed route-contract validation (`route-contract-v1`), exact persisted-write and FTS audits, databases and files on tmpfs, 64 MB response cache. The harness was run as uid 1000, as the app image's user. The exact orchestration scripts (`harness/run-bench.sh`, `run-bench2.sh`, `pass2.sh`) and the Linux host's Dockerfile are in the raw archive (see Raw data).

| Block | Image | Mac 1-min load before | VM 1-min load before | Start |
|---|---|---:|---:|---|
| 05-normal-main | main | 11.06 | 10.36 | 11:17:41 |
| 06-normal-b | B | 9.32 | 8.65 | 11:19:31 |
| 07-normal-b | B | 9.89 | 11.41 | 11:21:22 |
| 08-normal-main | main | 20.06 | 9.02 | 11:23:13 |
| 13-mixed-main | main | 21.70 | 10.62 | 11:31:10 |
| 14-mixed-b | B | 16.25 | 11.52 | 11:32:41 |
| 15-mixed-b | B | 11.89 | 9.96 | 11:34:12 |
| 16-mixed-main | main | 9.72 | 9.27 | 11:35:42 |
| 21-normal-main | main | 11.28 | 10.22 | 11:44:56 |
| 22-normal-b | B | 8.95 | 10.58 | 11:46:47 |
| 23-normal-b | B | 7.72 | 9.05 | 11:48:38 |
| 24-normal-main | main | 8.46 | 8.37 | 11:50:29 |

The Mac has 10 cores. The A comparison (`single-session-activity-query`) ran interleaved in the same session as blocks 01–04, 09–12 and 17–20 with its own `main` baselines; it is reported in `../20261009-single-session-activity-query/`. Both changes now ship together; each was measured alone against `main`, so these two folders give the per-change attribution.

## Results

Requests/sec at 16 clients, median (min–max) per route.

Normal profile, 8 samples per side (blocks 05–08 and 21–24):

| Route | main | B | Change |
|---|---:|---:|---:|
| Room page | 22,604 (19,177–25,430) | 25,168 (21,193–26,033) | +11.3% |
| Messages page | 24,456 (19,153–26,573) | 25,379 (13,220–26,695) | +3.8% |
| Sidebar | 26,586 (15,635–28,568) | 27,862 (4,578–28,670) | +4.8% |
| Search | 26,471 (6,199–28,416) | 26,960 (22,109–27,867) | +1.8% |
| Post a message | 3,654 (2,195–3,772) | 3,862 (3,542–3,963) | +5.7% |

The second, quieter pass alone (blocks 21–24, Mac load 7.7–11.3): room 22,772 (22,447–22,871) → 25,563 (22,162–26,033), +12.3%; posting 3,699 (3,639–3,754) → 3,930 (3,660–3,963), +6.2%; sidebar +3.9%, messages +0.9%, search +0.9%. In that pass the candidate's room and posting medians are above every baseline sample; the other three routes overlap.

Mixed-write profile, 4 samples per side (blocks 13–16), one writer capped at 10 posts/sec to HQ:

| Route | main | B | Change |
|---|---:|---:|---:|
| Room page | 20,432 (16,929–23,838) | 24,326 (23,941–24,635) | +19.1% |
| Messages page | 21,512 (18,878–23,626) | 24,739 (24,092–25,070) | +15.0% |
| Sidebar | 28,770 (23,713–31,495) | 31,920 (30,718–32,157) | +10.9% |
| Search | 28,045 (21,866–29,964) | 30,379 (29,937–30,653) | +8.3% |

The candidate's mixed samples are tight and above the baseline medians on all four routes, but baseline block 13 ran at a Mac load of 21.7 and only four samples per side exist, so the size of this gain is not established. Writers acknowledged 1,264 timed writes per side at 9.8–9.9 writes/sec.

Validation: 6,304,321 (main) and 6,559,730 (B) timed normal responses, 3,103,390 and 3,552,117 timed mixed reads, all with zero errors and zero invalid responses; every acknowledged write matched its database, rich-text and FTS rows. Peak generator CPU was 129–140% of the 400% available on its four CPUs.

## Peak memory

Sampled every 0.5 s inside the Linux host for the whole round (startup, login, contract preparation, warmups and all routes): the server process's peak RSS (`VmHWM`) and the container cgroup's `memory.peak`. Median (min–max) of the per-round peaks:

| Profile | Metric | main | B |
|---|---|---:|---:|
| Normal (8 rounds each) | Server peak RSS | 133.9 MiB (131.7–135.7) | 135.1 MiB (132.3–136.4) |
| Normal (8 rounds each) | cgroup memory.peak | 136.9 MiB (132.8–139.7) | 140.5 MiB (133.4–160.1) |
| Mixed (4 rounds each) | Server peak RSS | 135.4 MiB (133.6–137.2) | 133.4 MiB (129.7–134.9) |
| Mixed (4 rounds each) | cgroup memory.peak | 124.7 MiB (122.4–134.6) | 121.7 MiB (118.3–138.1) |

Turning off memory statistics only removes SQLite's counters; it does not change allocation, and the peaks agree within about 1 MiB of process RSS. One sampler line in the session log was taken after a process had exited; The analysis excludes such lines.

## The flag reaches SQLite

`PRAGMA compile_options` on a connection opened through `database.Open` lists `DEFAULT_MEMSTATUS=0` when built with this branch's `CGO_CFLAGS`, and no `DEFAULT_MEMSTATUS` entry with Go's default `-O2 -g` (checked with a throwaway test that was not kept).

## Images and sources

Both images were built with `docker build --platform linux/arm64` from the repository's own Dockerfile, with `APP_VERSION=bench-<tag>` and `GIT_REVISION=9b0ef062ea9732c6f611b705d77b52aeec31301f`.

| Image | Source | Image ID in Docker Desktop | Image ID in the bench host | `/usr/local/bin/campfire` sha256 |
|---|---|---|---|---|
| main | `9b0ef06`, clean | `sha256:210c69b5546a8e01c955fdb6752c8e2d38aa848381156722187be02111fbc36c` | `sha256:21efd64e14aa3dd753915c82e14cf7d8ae71ee14f01ee3cccd3b56c34924bd5d` | `4d20ecbd77d71ba6acef7bde9781cb6ff6fd8da9403bfb7fcbcc9eb54789582d` |
| B | `9b0ef06` plus only the build-flag change (measured before the two changes were combined) | `sha256:dd1675173d262682b335302d5d85b06ae2e6fb88208d3161d7790b300a400f16` | `sha256:b82024a1b0e67bcbbe56179ccad3abd75f0b1376353411a64a96712f05c6844f` | `88ea69b1217988c577fa7a185e3cc9a1d7c29720e8e46f53ba9bdb6ade994649` |

Docker Desktop's image store and the bench host's classic store report different IDs for the same `docker save`/`docker load` image, so the binary hash was compared inside both; it matches. The harness metadata records the bench-host IDs and `source_revisions.go.dirty: true` for B, because the change was measured uncommitted.

Seed: built by the harness's `bin/seed` (Rails `90b3300`, generated locally with fresh disposable credentials), SHA-256 `8dcc238c1888e74f87b78b15ee3e647661b05734e6cb5955558f0a1822f5b25c` (`logs/seed-source.json` in the raw archive). Loadgen: built from the harness's `loadgen/` with `rust:1-alpine` for linux/arm64 musl, SHA-256 `9d72dddf12dd8362c4f01036768dfed5d44120416f61eade123262b6301e61fc`. Both differ from the published report's hashes because they were built here.

## Limitations

- Docker Desktop 29.1.3 on an Apple M1 Max (arm64), Linux VM with 10 vCPUs and 8 GB. The harness and its loadgen ran unmodified inside a privileged `docker:29-dind` container in that VM, which gives it `taskset`, `/proc/loadavg`, host networking and tmpfs; the app containers ran in that inner Docker.
- VM CPUs were pinned (server 2–5, generator 6–9, memory sampler 0–1), but the VM's vCPUs share the Mac's cores with everything else on the Mac. The host was not idle: other sessions ran Go builds and tests (Mac load 7–22 during these blocks), and the shared ClickHouse/HyperDX/Hatchet/Redis/Mailpit/RustFS containers ran in the same VM throughout (VM load 8–12 before each block). Single samples dropped by up to 85% when the Mac was busy, which is why some ranges are wide.
- Relative before/after only, within one session. The absolute numbers are not comparable with the published table (AMD, native Linux, CPUs 8–11) and must not be presented as such. The mutex this removes is contended more as more threads allocate at once, so the effect may differ on the published four-CPU Linux setup.
- One viewer session per round, warm caches; cold reads, multiple viewers, Action Cable, TLS and durability on real disks were not measured.
- Each block runs two rounds back to back, so baseline and candidate are interleaved by block rather than by round.

## Failed attempts, kept

- The first block failed to start: the harness ran as root inside the bench host, so the databases it copied were read-only to the app's uid 1000 (`attempt to write a readonly database`). Fixed by running the harness as uid 1000; no sample was taken.
- The next attempt completed round 1 of block 01 (a `main` block of the A comparison) and then stopped with `go: source changed between rounds` (the harness's own source-identity guard). The cause was not identified; a later check of the same mount showed a stable HEAD and clean status. The run was restarted from block 01; that attempt's log is in `logs/failed-attempt/` in the raw archive, and none of its samples are used.

## Files

- `samples.tsv`: every timed sample, one row each: change, block, image (`main` or `candidate`), profile, round, route, clients, req/s, validated responses, invalid responses, errors, Mac 1-minute load before the block, VM 1-minute load at round start, the round's peak server RSS and cgroup `memory.peak`, and for the mixed profile the writer's acknowledged writes and rate. Its medians, ranges, totals and peak-memory figures reproduce the tables above exactly.
- `analysis.md`: the summary tables computed from the raw blocks.

## Raw data

The full harness output (per-block `summary.json`, `go-*.json`, `raw/http-*.json`), console logs, memory samples, image identities, the failed attempt's log, and the orchestration and analysis scripts for both changes in this PR: [campfire-go-pr11-bench-raw-20261009.tar.gz](https://github.com/khanakia/once-campfire-go/releases/download/pr11-bench-raw-20261009/campfire-go-pr11-bench-raw-20261009.tar.gz), sha256 `cb0f8e34a4d73cfd2c64d7796707528bff33031574610ad09e151d56effb7ca8`.
