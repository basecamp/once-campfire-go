# Go main, spliced gzip and Rust on Fly.io — 2026-10-05

`main` at `504428a` (`go-before`), `gzip-fragments` at `47989f5` (`go`) and Rust at `64f8635`
(`rust`) on one Fly.io `performance-16x` Machine in `iad`: 16 dedicated vCPUs (Intel Xeon,
family 6 model 106, 2.3 GHz), 32 GB. The three binaries ran on the same Machine, in rotating
order, on fresh copies of the same parity seed.

## Results

Median requests/sec at 16 clients, six repetitions.

With gzip:

| Workload | Go main | Go branch | Rust | Branch ÷ main | Rust ÷ branch |
|---|---:|---:|---:|---:|---:|
| Room page | 1,409 | 7,982 | 11,208 | 5.67× | 1.40× |
| Message history | 1,926 | 8,844 | 12,598 | 4.59× | 1.42× |
| Sidebar | 5,463 | 7,089 | 11,271 | 1.30× | 1.59× |
| Search | 2,447 | 4,980 | 10,000 | 2.04× | 2.01× |
| Post message | 2,146 | 2,145 | 2,178 | 1.00× | 1.02× |

With identity encoding:

| Workload | Go main | Go branch | Rust | Branch ÷ main | Rust ÷ branch |
|---|---:|---:|---:|---:|---:|
| Room page | 5,506 | 6,932 | 9,184 | 1.26× | 1.32× |
| Message history | 7,629 | 7,753 | 10,055 | 1.02× | 1.30× |
| Sidebar | 6,191 | 7,004 | 11,441 | 1.13× | 1.63× |
| Search | 4,990 | 5,138 | 9,807 | 1.03× | 1.91× |
| Post message | 2,034 | 2,012 | 2,007 | 0.99× | 1.00× |

Room-page p99 latency with gzip: 36.1 ms on main, 5.7 ms on the branch, 2.6 ms on Rust.
Post-message p99 is 28 ms on both Go binaries and 100 ms on Rust (133 ms with identity), though
Rust's p90 is lower (7.4 against 15.5 ms).

With gzip the Rust lead over the branch is the same 1.3-1.4× on rooms and history that it is
with identity, so what remains there is not compression. Search and the sidebar are 2.0× and
1.6× behind either way.

All 36 application runs completed with zero HTTP errors, 541,277 acknowledged writes verified in
both messages and FTS, and 36 thumbnails with identical hashes across the three binaries. Ranges
and latency are in the [gzip](gzip/report.md) and [identity](identity/report.md) reports.

## Measurement

```sh
bench/application --rust-root /bench/rust --out bench/results/fly-gzip1 \
  --apps go-before go rust --baseline-go /bench/campfire-main \
  --routes room_show messages_page sidebar search post_message --concurrency 16 \
  --cable-clients --upload-reps 1 --reps 6 --seconds 5 --gzip 1
```

The identity pass repeats this with `--gzip 0`. Server on CPUs 8-11, load generator on CPUs
12-15, direct application listener for all three, `Accept-Encoding: gzip` from the reference
load generator.

The image ([Dockerfile](Dockerfile), [run](run)) is the pinned `toolchain` stage plus both Go
binaries built in it, the Rust sources, the reference load generator built with Rust 1.98.1 and
the parity seed built by `parity/bin/seed build default` with
`ghcr.io/basecamp/once-campfire:sha-90b3300` as the reference app image. `run` installs Rust
1.98.1 with rustup on the Machine and builds Rust there with the reference Dockerfile's command
(`LIBRARY_PATH=/opt/vips/lib cargo build --release --locked -p campfire`) against the toolchain
stage's libvips. [machine.txt](machine.txt) is the Machine's `lscpu`.

## Limitations

- A virtual machine on shared hardware, not the workstation behind the README figures: absolute
  rates are not comparable with them, or with other Fly.io runs, which landed on AMD EPYC hosts.
  Only the ratios on this Machine are.
- Rust was built on the Machine with a toolchain fetched at run time, not from the Rust
  production image; its libvips is the Go toolchain stage's build of the same Debian source.
- The pages differ between the ports: Rust's room page is 416 KB (24 KB gzipped), Go's 374 KB
  (22 KB); Rust's sidebar is 31 KB, Go's 9 KB.
- `mise` and `git` in the image are shims that run the tool on `PATH` and report the pinned
  reference commit.
- The load average in each `metadata.json` includes what preceded the pass (the Rust build, then
  the gzip pass); nothing else ran on the Machine.
- Warm-cache reads of one room, one history page, one sidebar and one search. Cable throughput,
  other client counts, the front server, TLS and public compression were not measured.
