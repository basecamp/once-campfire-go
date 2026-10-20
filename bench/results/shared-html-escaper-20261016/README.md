# Share the fixed HTML escaper — 2026-10-16

Attachment and sound rendering now reuse the existing fixed `strings.Replacer`
instead of rebuilding its table for every escaped value. The five replacements
and their entity spellings are unchanged. The standard-library replacer supports
concurrent use. URL filtering, sanitization, templates, signing, data/cache/queue/GC
policies and media byte processing are unchanged.

Baseline: local `5af6df6`, runtime-equivalent to published PR head `4dfa3fd`.
One candidate was tested. A baseline uncached forty-file-message application-query
profile attributes **50.9% of sampled allocated bytes** to `strings.Replacer.build`.
The query includes SQLite authorization/relationships, URL signing and complete
message markup; it is not a standalone escaper benchmark. The candidate profile
confirms that repeated table construction disappears. Cumulative profile shares
are diagnostic, not wall-time or recoverable-savings bounds.

## Measurements

Three sequential alternating query/render pairs:

| Forty-message case | Baseline time | Candidate time | Allocated bytes, baseline → candidate |
|---|---:|---:|---:|
| File attachments, uncached | 1,435.766 µs | 1,028.409 µs (−28.4%) | 3,657,438 → 1,714,403 (−53.1%) |
| Image attachments, uncached | 1,393.495 µs | 1,289.066 µs (−7.5%) | 2,860,472 → 2,031,516 (−29.0%) |
| Plain text, uncached | 672.280 µs | 665.279 µs (flat) | 1,272,274 → 1,271,706 (flat) |
| File attachments, warm hit | 36.753 µs | 36.672 µs (flat) | 17,919 → 17,918 (flat) |

The cold query probe uses the existing zero-generation observation contract,
without changing production cache settings. Warm query controls use ordinary
observation/admission and the default 32 MiB fragment budget.

Complete public-listener controls use disposable copies of the shared seed, forty
new canonical messages and its real text/image blobs. HTML rendering does not fetch
or transform those media bytes. Each process performs 2,001 complete identity GETs
including preflight, 500 warm and 1,500 measured reads. Cold cases commit an independent
ignored account-settings nonce before each GET; the warm control does not. Every
body is fully read and checked against its origin-normalized SHA-256 across all arms,
including warm/cold file agreement. All **48,024 responses** match. Expected message
IDs, escaped hostile filename text, lightbox markup, populations, seed FK exceptions,
integrity and clean joined exits pass.

| Median joined server CPU, three alternating pairs | Baseline | Candidate | Change |
|---|---:|---:|---:|
| File attachments, foreign-invalidated | 3.897912 s | 3.437805 s | **−11.80%** |
| Image attachments, foreign-invalidated | 3.929511 s | 3.796265 s | −3.39% |
| Plain text, foreign-invalidated | 2.617798 s | 2.623015 s | +0.20%, flat |
| File attachments, warm hit | 0.586676 s | 0.585413 s | −0.22%, flat |

Every file-target pair improves. All declared query and complete-response gates
pass; minor adverse plain/warm repetitions are retained. Joined `wait4` CPU includes
startup, login and shutdown; fixture/client/foreign-writer CPU is excluded. These
are local **Apple M3 Max/macOS arm64/Go 1.27.1**, CGO/FTS5 work measurements with four
runtime workers and unchanged production settings. No task-owned agent, test, build
or profiler overlaps uninstrumented timing; other host activity is not exclusively
controlled. Three short pairs do not establish confidence bounds. The gain concerns
attachment-page misses, not general warm-hit capacity, native Fedora rankings,
image/video processing or a measured sound-message speedup.

## Verification and retained data

- Full race-enabled presentation/web suites and `bin/check` pass, including asset
  generation, Python regressions, root vet/races and nested WebSocket races.
- A focused concurrent-use case protects exact entities, already-escaped text,
  plus/NUL, Unicode separators and invalid UTF-8 bytes in the affected helper.
- Existing Go/Rust attachment-lifecycle, storage-download, message-lifecycle and
  pagination HTTP/state workflows pass **8/8**. This is not a fresh full browser
  or independent media-output certification; media processing is unchanged.
- Removing the diagnostic probe leaves a byte-identical measured/tested executable:
  `69aa2e894e10a2d04556ecb6e1f227f216edb714745c914ab04e9a7060575627`.

`measurements.json.gz` retains budgets, complete per-run server records, all query
repetitions, original source/binary/seed identities and profile-owner summaries.
Compressed probes/scripts reproduce the diagnostic setup; they are not permanent
suite additions. `manifest.json` hashes the retained artifacts. Runtime logs,
credentials/cookies, generated fixture databases/storage and compiled binaries are
excluded. Raw profiles stay private. Do not add these workload-specific percentages
to earlier improvements or treat them as refreshed POST/WebSocket comparisons.
