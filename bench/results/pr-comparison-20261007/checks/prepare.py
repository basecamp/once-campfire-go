import hashlib,json,subprocess
from pathlib import Path
root=Path('/src'); dst=root/'.cache/competitor-builds-arm'
h=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
heads={'pr-2':'8a374d7600cc37500509730a3d62fb9103315ee4','pr-4':'3b2054f9dac4b50c5bc1a6a29d8cd05992133034','pr-5':'f2c50e0fcf3efa1454bedb2741c3c54e0374f6f7','pr-6':'99138d721101b43180bf4541af0aee1c6645bdad','pr-7':'18e3b236b9c7f535b379d58d799cc8c77d54d4fb','pr-8':'0f7e0e0e68a8a4f1f0eab9b23cee01d39dc1761d','ours-before':'85c302f (production 7afad06)','ours-slots':'85c302f plus uncommitted guarded template-slot renderer'}
result={'image':'campfire-compare-toolchain (67d2d1d03551a5667fca84d0313c33a427d176a0a399cd96c1e8ca16487c6d3c)','variants':{}}
for name,head in heads.items():
    source=root/'.cache/competitor-stage'/name; binary=dst/name
    files={str(p.relative_to(source)):h(p) for folder in ('cmd','internal','assets','third_party') for p in sorted((source/folder).rglob('*')) if p.is_file()}
    for f in ('go.mod','go.sum','bin/build-assets'):files[f]=h(source/f)
    result['variants'][name]={'head':head,'source_files_sha256':files,'binary_sha256':h(binary),'bytes':binary.stat().st_size,'go_build_info':subprocess.check_output(['go','version','-m',str(binary)],text=True),'check_log_sha256':h(dst/(name+'-check.log')),'GOGC':'environment unset; PR8 run() sets 200' if name=='pr-8' else 'environment unset (Go default 100)','PGO':'automatic default.pgo when present','sqlite_fts5_tag':name!='pr-8'}
result['rust']={'head':subprocess.check_output(['git','-C','reference','rev-parse','HEAD'],text=True).strip(),'binary_sha256':h(root/'.cache/rust-linux-target/release/campfire'),'lock_sha256':h(root/'reference/Cargo.lock'),'sources_sha256':{str(p.relative_to(root/'reference')):h(p) for p in sorted((root/'reference/crates').rglob('*')) if p.is_file()}}
result['rate_loadgen']={'binary_sha256':h(dst/'httprate'),'sources_sha256':{str(p.relative_to(root)):h(p) for p in sorted((root/'bench/httprate').glob('*.go'))}}
(dst/'manifest.json').write_text(json.dumps(result,indent=2)+'\n')
