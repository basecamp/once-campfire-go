# Go main, spliced gzip and Rust on Fly.io — 2026-10-05

`main` at `504428a` (`go-before`), `gzip-fragments` at `47989f5` (`go`) and Rust at `64f8635`
(`rust`) on one Fly.io `performance-16x` Machine in `iad`: 16 dedicated vCPUs (AMD EPYC,
family 25 model 1), 32 GB. The three binaries ran on the same Machine, in rotating
order, on fresh copies of the same parity seed.

## Results

Median requests/sec at 16 clients, six repetitions.

With gzip:

| Workload | Go main | Go branch | Rust | Branch ÷ main | Rust ÷ branch |
|---|---:|---:|---:|---:|---:|
| Room page | 1,282 | 6,678 | 8,424 | 5.21× | 1.26× |
| Message history | 1,646 | 7,277 | 8,974 | 4.42× | 1.23× |
| Sidebar | 4,486 | 5,148 | 8,284 | 1.15× | 1.61× |
| Search | 2,233 | 4,148 | 8,037 | 1.86× | 1.94× |
| Post message | 1,874 | 1,906 | 1,969 | 1.02× | 1.03× |

With identity encoding:

| Workload | Go main | Go branch | Rust | Branch ÷ main | Rust ÷ branch |
|---|---:|---:|---:|---:|---:|
| Room page | 4,313 | 5,428 | 6,437 | 1.26× | 1.19× |
| Message history | 5,984 | 5,937 | 6,851 | 0.99× | 1.15× |
| Sidebar | 4,585 | 5,129 | 8,190 | 1.12× | 1.60× |
| Search | 4,018 | 4,087 | 7,351 | 1.02× | 1.80× |
| Post message | 1,945 | 1,934 | 2,090 | 0.99× | 1.08× |

Room-page p99 latency with gzip: 42.0 ms on main, 6.4 ms on the branch, 3.4 ms on Rust.
Post-message p99 is 32 ms on both Go binaries and 14 ms on Rust (31 and 13 ms with identity).

With gzip the Rust lead over the branch on rooms and history is 1.2-1.3×, against 1.15-1.2× with
identity, so most of what remains there is not compression. Search and the sidebar are 1.8-1.9×
and 1.6× behind either way.

All 36 application runs completed with zero HTTP errors, 482,133 acknowledged writes verified in
both messages and FTS, and 36 thumbnails with identical hashes across the three binaries. Ranges
and latency are in the [gzip](gzip/report.md) and [identity](identity/report.md) reports.

## Measurement

```sh
bench/application --rust-root /bench/rust --out bench/results/fly-gzip1 \
  --apps go-before go rust --baseline-go /bench/campfire-main \
  --routes room_show messages_page sidebar search post_message --concurrency 16 \
  --cable-clients --upload-reps 1 --reps 6 --seconds 5 --gzip 1 \
  --host-label 'Fly.io performance-16x Machine'
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
  rates are not comparable with them, or with other Fly.io runs. Only the ratios on this Machine
  are, and they move with the host too: the same image first ran on an Intel Xeon host (family 6
  model 106; results at `9da6230`), where read rates were 1.1-1.5× higher, Rust ÷ branch on the
  gzip room page was 1.40× and Rust's post-message p99 was 100 ms.
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
