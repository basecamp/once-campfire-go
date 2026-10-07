#!/usr/bin/env bash
set -euo pipefail
common=(--rust-root reference --seed .cache/parity-seeds/default --loadgen .cache/linux/loadgen --gzip 1 --listener public --concurrency 16 --reps 3 --seconds 10 --server-cpus 0-3 --loadgen-cpus 4-7 --upload-reps 0)
variants=(--candidate before=.cache/competitor-builds-arm/ours-before --candidate slots=.cache/competitor-builds-arm/ours-slots)
archive() {
 python3 - "$1" <<'PY'
import gzip,sys
from pathlib import Path
name=sys.argv[1]; src=Path('/tmp')/name;dst=Path('/src/.cache/pr-comparison-results')/name;dst.mkdir(parents=True)
for p in [*src.iterdir(),Path('/tmp')/(name+'.log')]:
 if p.is_file():
  if p.name.endswith('.log'):(dst/(p.name+'.gz')).write_bytes(gzip.compress(p.read_bytes(),compresslevel=9,mtime=0))
  else:(dst/p.name).write_bytes(p.read_bytes())
PY
}
run() {
 local name=$1;shift
 python3 bench/application "${common[@]}" "$@" --out "/tmp/$name" > "/tmp/$name.log" 2>&1
 archive "$name"
 tail -5 "/tmp/$name.log"
}
case ${1:-} in
 candidate)
  run slots-warm "${variants[@]}" --apps before slots --routes room_show messages_page search post_message --same-html-apps before slots --cable-clients
  run slots-cold "${variants[@]}" --apps before slots --routes room_show messages_page search post_message --same-html-apps before slots --fragment-cache-mb 0 --cable-clients
  ;;
 competitors)
  all=(--candidate ours=.cache/competitor-builds-arm/ours-slots --candidate rust=.cache/rust-linux-target/release/campfire)
  for pr in 2 4 5 6 7 8; do all+=(--candidate "pr-$pr=.cache/competitor-builds-arm/pr-$pr");done
  run competitors-capacity "${all[@]}" --apps ours pr-2 pr-4 pr-5 pr-6 pr-7 pr-8 rust --routes room_show messages_page search post_message --mask-legacy-room-cursor --cable-clients
  ;;
 rates)
  all=(--candidate ours=.cache/competitor-builds-arm/ours-slots --candidate rust=.cache/rust-linux-target/release/campfire)
  for pr in 2 4 5 6 7 8; do all+=(--candidate "pr-$pr=.cache/competitor-builds-arm/pr-$pr");done
  run competitors-rate-room "${all[@]}" --apps ours pr-2 pr-4 pr-5 pr-6 pr-7 pr-8 rust --routes room_show --mask-legacy-room-cursor --rate-loadgen .cache/competitor-builds-arm/httprate --http-rates 500 --cable-clients
  run competitors-rate-search "${all[@]}" --apps ours pr-2 pr-4 pr-5 pr-6 pr-7 pr-8 rust --routes search --rate-loadgen .cache/competitor-builds-arm/httprate --http-rates 5000 --cable-clients
  run competitors-rate-posts "${all[@]}" --apps ours pr-2 pr-4 pr-5 pr-6 pr-7 pr-8 rust --routes post_message --rate-loadgen .cache/competitor-builds-arm/httprate --http-rates 1000 --cable-clients
  ;;
 cold)
  all=(--candidate ours=.cache/competitor-builds-arm/ours-slots --candidate rust=.cache/rust-linux-target/release/campfire)
  for pr in 2 4 5 6 7 8; do all+=(--candidate "pr-$pr=.cache/competitor-builds-arm/pr-$pr");done
  run competitors-cold "${all[@]}" --apps ours pr-2 pr-4 pr-5 pr-6 pr-7 pr-8 rust --routes room_show messages_page search --mask-legacy-room-cursor --fragment-cache-mb 0 --cable-clients
  ;;
 active)
  run slots-active "${variants[@]}" --apps before slots --routes active_room active_search --cable-clients
  ;;
 cable)
  run slots-cable "${variants[@]}" --apps before slots --routes post_message --cable-clients 1000 --deflate 0 1 --cable-seconds 5
  ;;
 gc)
  export GOGC=100
  run pr8-gc100 --candidate ours=.cache/competitor-builds-arm/ours-slots --candidate pr-8=.cache/competitor-builds-arm/pr-8 --candidate rust=.cache/rust-linux-target/release/campfire --apps ours pr-8 rust --routes room_show messages_page search post_message --cable-clients
  ;;
 paced)
  run slots-paced-cable "${variants[@]}" --apps before slots --routes room_show --cable-clients 1000 --deflate 0 1 --cable-seconds 0
  ;;
 *) echo 'usage: pr-comparison-run.sh candidate|competitors|rates|cold|active|cable|paced|gc' >&2;exit 2;;
esac
