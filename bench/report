#!/usr/bin/env python3
"""Summarize completed application runs without rerunning the benchmark."""
import json,statistics,sys
from pathlib import Path
out=Path(sys.argv[1]);meta=json.loads((out/'metadata.json').read_text());runs=json.loads((out/'raw.json').read_text());settings=meta['settings']
median=lambda xs:statistics.median(xs)
fixed=bool(settings.get('http_rates'))
def spread(xs,places=0):return f'{median(xs):,.{places}f} ({min(xs):,.{places}f}–{max(xs):,.{places}f})'
lines=['# Campfire application benchmark','',meta['scope'],'',f"{settings['reps']} {'rotating' if len(settings.get('apps', [])) > 2 else 'alternating'} repetitions; {settings['seconds']}-second HTTP samples after 2-second warmups. Four application workers on CPUs {settings['server_cpus']}; load generator on CPUs {settings['loadgen_cpus']}. HTTP uses {'gzip' if settings.get('gzip') else 'identity'} encoding. Every run starts with a separate copy of the same seed.",'','HTTP response contracts compare message IDs, sidebar room IDs, avatar bytes and CSS bytes. Opt-in message forms require complete document layouts and seed-grounded editor/attachment/ordered boost controls; selected applications also require exact decoded form bytes after normalization of only their listener origin. These are full-document GETs, not Turbo-Frame click measurements. Write checks require every successful request to persist and enter the FTS index. Cable checks require every client to subscribe and every message to arrive at every client. Upload checks fetch the actual representation and compare its bytes across applications.','', '## HTTP','', 'Rates show median (minimum–maximum); latency is the median of each run’s percentile.','', '| Workload | Clients | App | Requests/s (range) | p50 ms | p90 ms | p99 ms | CPU µs/request |','|---|---:|---|---:|---:|---:|---:|---:|']
if fixed:
 lines=lines[:-2]+['| Workload | Max clients | Offered req/s | App | Successful req/s (range) | Scheduled p50 ms | p95 ms | p99 ms | Generator p99 ms | Queue p99 ms | Service p99 ms | CPU µs/request |', '|---|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|']
 lines.insert(lines.index('## HTTP')+2,'Fixed-rate arrivals are independent of completions. Latency starts at the scheduled arrival, including generator lateness and client queueing; service latency alone would omit these delays. Quantiles are upper bucket bounds (1 µs resolution below 1 ms, <0.2% above). Successful-response percentiles must be read with error/drop/expiry counters in raw samples. Samples with any such failures are rejected and retained separately. Throughput includes drain time when completion exceeds the arrival window. Generator lateness and queue delay disclose client-side limitations; these are not server-only latency measurements.\n')
keys=list(dict.fromkeys((s['route'],s['conc'],s.get('offered_rps')) for r in runs for s in r['http']))
apps=list(dict.fromkeys(r['app'] for r in runs))
for name,c,rate in keys:
 for app in apps:
  samples=[s for r in runs if r['app']==app for s in r['http'] if s['route']==name and s['conc']==c and s.get('offered_rps')==rate]
  if not samples:continue
  cpu=f'{median(s["cpu_us_per_request"] for s in samples):.1f}' if all('cpu_us_per_request' in s for s in samples) else '—'
  if fixed:
   latencies=' | '.join(f'{median(s["latency"][p] for s in samples):.3f}' for p in ('p50_ms','p95_ms','p99_ms'))
   components=' | '.join(f'{median(s[k]["p99_ms"] for s in samples):.3f}' for k in ('generator_lateness','queue_latency','service_latency'))
   lines.append(f'| {name} | {c} | {rate:g} | {app} | {spread([s["rps"] for s in samples])} | {latencies} | {components} | {cpu} |')
  else:
   lines.append(f'| {name} | {c} | {app} | {spread([s["rps"] for s in samples])} | '+ ' | '.join(f'{median(s["latency"][p] for s in samples):.3f}' for p in ['p50_ms','p90_ms','p99_ms'])+f' | {cpu} |')
lines+=['','## Action Cable','','Throughput counts posted messages delivered to **all** clients; one such message produces one frame per client. Latency includes delivery, rather than just accepting a post. The reference load generator reports p90 rather than p95.','','| Clients | Deflate | App | Messages/s (range) | Frames/s | Per-client p99 ms | All-clients p99 ms | Wire MB/s |','|---:|---|---|---:|---:|---:|---:|---:|']
keys=list(dict.fromkeys((s['clients'],s.get('deflate',False)) for r in runs for s in r['cable']))
for n,compression in keys:
 for app in apps:
  samples=[s['throughput'] for r in runs if r['app']==app for s in r['cable'] if s['clients']==n and s.get('deflate',False)==compression]
  if not samples:continue
  percentile=lambda key:f'{median(s[key]["p99_ms"] for s in samples):.3f}' if all('p99_ms' in s[key] for s in samples) else '—'
  lines.append(f'| {n:,} | {compression} | {app} | {spread([s["delivered_msgs_per_sec"] for s in samples],1)} | {median(s["frames_per_sec"] for s in samples):,.0f} | {percentile("per_client")} | {percentile("all_clients")} | '+(f'{median(s["wire_mb_per_sec"] for s in samples):.1f}' if compression else 'not measured')+' |')
lines+=['','## Media and resources','','| App | Upload + thumbnail median ms | Thumbnail bytes | Startup median ms | Idle Pss MiB | After HTTP Pss MiB | Final Pss MiB | Binary MiB |','|---|---:|---:|---:|---:|---:|---:|---:|']
for app in apps:
 samples=[r for r in runs if r['app']==app];uploads=[r['upload'] for r in samples if 'upload' in r]
 valid_media=uploads and 'thumb_sha256' in uploads[0]['runs'][0]
 media=f'{median(r["median_total_ms"] for r in uploads):.2f}' if valid_media else 'not validated'
 thumb=str(uploads[0]['runs'][0]['thumb_bytes']) if valid_media else '—'
 mem=lambda k:median(r[k]['Pss'] for r in samples)/1024
 lines.append(f'| {app} | {media} | {thumb} | {median(r["startup_ms"] for r in samples):.1f} | {mem("idle_memory_kib"):.1f} | {mem("http_memory_kib"):.1f} | {mem("final_memory_kib"):.1f} | {meta["binaries"][app]["bytes"]/1024**2:.1f} |')
lines+=['','Startup includes application initialization, measured to a successful health request at 25 ms polling intervals. Final memory follows the complete workload sequence and includes allocator high-water effects; it is not a per-client memory measurement. Each media sample creates a new blob and fetches its generated thumbnail.','', '## Reproduction and limits','', '- [Raw samples](raw.json) include statuses, errors, latency distributions, delivery counters, byte hashes and memory snapshots. [Metadata](metadata.json) records source/binary/seed hashes, CPU details, affinity and load averages.', '- These are local workstation measurements, sequential within the harness. Background host activity is recorded, not eliminated. They are not a language-wide performance claim.', f'- This comparison uses the {settings.get("listener", "application")} HTTP listener and {"gzip" if settings.get("gzip") else "identity"} encoding. It does not measure TLS/ACME or zstd throughput. Active-room CPU includes the concurrent mutation writer.', '- Sidebar room-ID checks do not validate application/Turbo-frame layout completeness; verify that scope separately before any cross-port sidebar comparison.', '- Screen-level HTML and network comparisons remain stricter than the functional response contracts used here. Benchmark validation is not a declaration of complete byte-for-byte UI parity.', '']
cable_validation = ('Incomplete throughput deliveries: '+str(sum(s['throughput']['posted']-s['throughput']['complete'] for r in runs for s in r['cable']))+'.') if any(r['cable'] for r in runs) else 'Cable throughput was not measured.'
writes=sum(r.get('write_validation',{}).get('persisted',0) for r in runs)
active=sum(r.get('active_validation',{}).get('persisted',0)+r.get('active_search_validation',{}).get('persisted',0) for r in runs)
thumbs=sum(len(r['upload']['runs']) for r in runs if 'upload' in r and 'thumb_sha256' in r['upload']['runs'][0])
lines += ['## Validation totals', '', f"{len(runs)} completed application runs; {writes:,} acknowledged HTTP posts and {active:,} active-room posts verified in messages and FTS; {thumbs} validated thumbnail samples. HTTP errors: {sum(s['errors'] for r in runs for s in r['http'])}. {cable_validation}", '', 'Initial full response sizes from the first repetition, before workload timing. Message/room ID and byte contracts are recorded in raw.json. Benchmark contracts do not establish browser or strict HTML parity.', '', '| Response | App | Plain bytes | Encoded bytes | Encoding |', '|---|---|---:|---:|---|']
for r in runs:
 if r['rep']==1:
  for name,body in r.get('bodies',{}).items():lines.append(f"| {name} | {r['app']} | {body['bytes']:,} | {body.get('encoded_bytes','—')} | {body.get('encoding','not recorded')} |")
lines += ['', 'Application logging is retained. Settings, native libraries, source/binary hashes and toolchains are recorded in metadata.json. Container media byte goldens require their separately pinned toolchain.', '']
if (out/'harness.json').exists():
 lines += ['[Harness hashes and build commands](harness.json). Native binary sizes reflect these build flags; the production Go image strips symbols. Server/load-generator logs are retained alongside the raw JSON (gzip-compressed after measurement when archived).', '']
if (out/'CONTENTION.md').exists():
 lines += ['[Interrupted repetitions and resumption](CONTENTION.md): incomplete contention-affected attempts were excluded and retried with unchanged binaries and settings. Completed samples were retained. See that record for host-noise limits.', '']
(out/'report.md').write_text('\n'.join(lines))
