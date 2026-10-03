# Port validation — 2026-10-02

The full application is implemented. Tests and browser workflows pass; the screenshot and accessibility layers match in the exercised inventory. **Strict all-layer parity is not a pass:** server/live DOM and network differences remain visible.

## Automated checks

| Check | Result |
|---|---|
| Formatting, generated assets, vet and race tests | [Pass](validation/check.txt) |
| 315 frontend asset paths/bodies, importmap and stylesheet tags | [Pass](validation/assets.txt) |
| Cookie interoperability in both directions; Rust reads/searches Go-written messages | [Pass](validation/upgrade.txt) |
| Chromium setup, two-tab messaging, editing, clipboard links, search, profiles/account, rooms, bots, styles, QR, transfers/invitations and direct pings | [Pass](validation/browser.txt) |
| Pinned-toolchain media byte goldens | [Pass](validation/media.txt) |
| Live ACME issuance, HTTPS and cached restart with CA offline | [Pass](validation/acme.txt) |
| Non-root production container, public HTTP, setup, live backup, offline restore and restart | [Pass](validation/container.txt) |

Package tests use fresh temporary databases. Integration tests did not skip for a missing seed. The upgrade and screen runs use disposable copies of the original seed. Media goldens use the pinned container versions; the browser and benchmark use the same native media libraries for both applications. ACME uses a local Pebble CA; Web Push delivery uses local tests rather than external providers.

## Screen inventory

The original Chromium inventory covers 214 desktop/light cells across default, first-run, custom-style, restricted-account and crowded-account seeds. Another 12 phone light/dark cells cover sign-in, account settings, message actions, open/direct rooms and history. No cells errored and no Go-specific masks or allowlists were added.

| Layer | Desktop matches | Desktop differences | Not applicable | Phone matches | Phone differences |
|---|---:|---:|---:|---:|---:|
| pixels | 198 | 0 | 16 | 12 | 0 |
| aria | 198 | 0 | 16 | 12 | 0 |
| server | 21 | 193 | 0 | 0 | 12 |
| live | 9 | 189 | 16 | 0 | 12 |
| network | 0 | 198 | 16 | 0 | 12 |
| cable | 197 | 1 | 16 | 12 | 0 |

Strict whole-cell results: 12 desktop passes, 202 desktop failures. A matching screenshot does not turn a DOM/network failure into a pass. Fragment/protocol-only cases do not have screenshot, live-DOM, accessibility or Cable layers.

The main run was followed by five focused captures after fixing thumbnail URL defaults, sidebar classes and the remote-disconnect reason. The table uses the later results for those five cells. Each retained report has its own binary hashes; this is an aggregate of the full run and focused verification, not a claim that the entire inventory was rerun after the last three fixes.

Raw layer reports: [default](validation/default.json), [first_run](validation/first_run.json), [custom_styles](validation/custom_styles.json), [restricted](validation/restricted.json), [crowd](validation/crowd.json), [phone](validation/phone.json), [phone_chat](validation/phone_chat.json), [cable_fixes](validation/cable_fixes.json). [Aggregate counts](validation/summary.json). Metadata files beside them record seed, frozen clock and Go/Rust binary hashes. Full local HTML reports/screenshots remain under `.cache/screens-*`.

## Differences retained

- HTML differences include empty input values, optional input-size attributes, whitespace, attribute serialization and canonical form-action URLs. See the [sign-in DOM example](validation/sign-in-server.norm.html.diff).
- Network differences include asset `Last-Modified` versus `Accept-Ranges`, response security/transfer headers and HTML-derived validators/body hashes. Assets themselves are byte-identical. See the [sign-in network comparison](validation/sign-in-network.txt.diff).
- The sole remaining desktop Cable difference is typing after room deletion: Go ignores commands for the deleted room, while Rust echoes them. Both render the deleted-room composer message. See the [Cable trace](validation/deleted-room-cable.diff).
- Exotic malformed parameter/content-negotiation combinations are not exhaustively equivalent. The tested routes, valid browser workflows, signing/storage contracts, and upgrade checks are the support for compatibility; they are not a proof for every possible HTTP input.

Known implementation choices are also recorded in [README.md](../README.md#known-differences). The benchmark uses explicit functional contracts rather than treating this stricter all-layer comparison as a pass.

## Optimization verification — 2026-10-03

The optimized binary passes [formatting, vet and race tests](validation/optimization/check.txt),
including the local WebSocket extension and upstream WebSocket tests. Added regression checks
cover sidebar cache invalidation, recorded message bytes/conditional responses, publication
session/membership revocation, and the focused plain-text path throughout the rich-text corpus.
[Browser workflows](validation/optimization/browser.txt), [bidirectional cookie/message upgrade](validation/optimization/upgrade.txt),
and [production container setup, backup, restore and restart](validation/optimization/container.txt)
pass with the optimized build.

A fresh complete default-seed desktop run covered 192 cells: 178/178 applicable screenshots
and accessibility comparisons matched, with zero capture errors. Server DOM matched 19/192,
live DOM 8/178, network 0/178, and Cable 177/178. The same deleted-room typing difference
remains. Eight phone light/dark cells all matched pixels, accessibility and Cable; their
DOM/network differences remain. Strict whole-cell parity therefore still fails.

Reports and binary hashes: [desktop](validation/optimization/default.json),
[desktop metadata](validation/optimization/default.metadata.json),
[phone](validation/optimization/phone.json), [phone metadata](validation/optimization/phone.metadata.json).
No new masks or allowlists were added. The earlier alternative-seed runs above were not repeated
for this optimization. Full local artifacts are in `.cache/screens-optimized*`.

The [optimized application benchmark](../bench/results/application-optimized-20261003/report.md)
completed all six application runs: 126 HTTP samples, 36 Cable configurations, 506,897
acknowledged HTTP writes checked in both messages and FTS, and 30 identical-byte thumbnails.
There were zero HTTP errors or incomplete deliveries. Interrupted repetitions affected by
external builds were excluded and rerun; the report records those interruptions and resumption.
