#!/usr/bin/env python3
"""Measure isolated applications with full-response and persistence validation."""
import re
import gzip, threading
import argparse, datetime, hashlib, json, math, os, platform, shutil, socket, sqlite3, statistics, subprocess, tempfile, time, urllib.request
from pathlib import Path
from message_forms import contract as form_contract, seed_message
FORM_ROUTES = {'message_edit', 'message_edit_attachment', 'new_boost', 'boosts_index'}
ROOT = Path(__file__).resolve().parents[1]
def digest(path): return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1',0)); return s.getsockname()[1]
def text(*args): return subprocess.check_output(args,text=True).strip()
def memory(pid):
    try:
        return {k:int(v.split()[0]) for k,v in (line.split(':',1) for line in Path(f'/proc/{pid}/smaps_rollup').read_text().splitlines()[1:]) if k in ('Rss','Pss','Private_Dirty')}
    except FileNotFoundError:return {}
def cpu_seconds(pid):
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    return (int(fields[11]) + int(fields[12])) / os.sysconf('SC_CLK_TCK')
def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--rust-root',type=Path,default=ROOT.parent/'once-campfire-rust')
    p.add_argument('--out',type=Path,required=True)
    p.add_argument('--seed',type=Path,help='reference parity seed directory')
    p.add_argument('--loadgen',type=Path,help='pinned reference load generator binary')
    p.add_argument('--rate-loadgen',type=Path,help='port-owned bench/httprate binary for fixed offered rates')
    p.add_argument('--http-rates',type=float,nargs='+',help='offered requests/sec; replaces capacity samples, requires one concurrency bound')
    p.add_argument('--cpu-profile',action='store_true',help='save one CPU profile per application/repetition')
    p.add_argument('--reps',type=int,default=3)
    p.add_argument('--baseline-go',type=Path,help='optional unchanged Go binary for before/after comparisons')
    p.add_argument('--go-binary',type=Path,default=ROOT/'campfire')
    p.add_argument('--gzip',type=int,choices=[0,1],default=0)
    p.add_argument('--listener',choices=['application','public'],default='application')
    p.add_argument('--mask-legacy-room-cursor',action='store_true',help='mask only the original render-time cursor defect when comparing pre-fix binaries')
    p.add_argument('--active-room-hz',type=float,default=20,help='message commits/sec during active_room/active_search reads')
    p.add_argument('--fragment-cache-mb',type=int,help='override application fragment retention; 0 exercises uncached rendering')
    p.add_argument('--routes',nargs='+',choices=['room_show','active_room','messages_page','sidebar','search','active_search','avatar','static_css','post_message',*sorted(FORM_ROUTES)])
    p.add_argument('--same-html-apps',nargs='+',default=[],help='require exact decoded response bytes across these applications after normalizing only each known listener origin')
    p.add_argument('--resume',action='store_true',help='resume completed runs with identical binaries and settings')
    p.add_argument('--seconds',type=float,default=5)
    p.add_argument('--concurrency',type=int,nargs='+',default=[1,16,64])
    p.add_argument('--cable-clients',type=int,nargs='*',default=[100,1000,10000])
    p.add_argument('--deflate',type=int,nargs='+',default=[0,1],choices=[0,1])
    p.add_argument('--cable-seconds',type=float,default=5)
    p.add_argument('--upload-reps',type=int,default=5)
    p.add_argument('--server-cpus',default='8-11')
    p.add_argument('--loadgen-cpus',default='12-15')
    p.add_argument('--candidate',action='append',default=[],metavar='NAME=PATH',help='additional named application binary')
    p.add_argument('--apps',nargs='+',default=['rust','go'])
    a=p.parse_args(); rust=a.rust_root.resolve(); out=a.out.resolve(); out.mkdir(parents=True,exist_ok=a.resume)
    seed=(a.seed or rust/'parity/.seed/default').resolve(); labels=json.loads((seed/'labels.json').read_text())
    binaries={'go':a.go_binary.resolve(),'rust':rust/'target/release/campfire'}
    if a.baseline_go:binaries['go-before']=a.baseline_go.resolve()
    for candidate in a.candidate:
        name,sep,path=candidate.partition('=')
        if not sep or not name or not path:p.error('--candidate requires NAME=PATH')
        binaries[name]=Path(path).resolve()
    if len(set(a.apps))!=len(a.apps):p.error('--apps must be unique')
    if any(app not in binaries for app in a.apps):p.error('unknown app: supply --candidate NAME=PATH or --baseline-go')
    if any(app not in a.apps for app in a.same_html_apps):p.error('--same-html-apps must be selected applications')
    binaries={app:binaries[app] for app in a.apps}
    lg=(a.loadgen or rust/'target/bench/release/loadgen').resolve()
    if a.http_rates:
        if not a.rate_loadgen or len(a.concurrency)!=1:p.error('--http-rates requires --rate-loadgen and one --concurrency bound')
        if any(not math.isfinite(rate) or rate<=0 for rate in a.http_rates):p.error('--http-rates must be finite and positive')
    elif a.rate_loadgen:p.error('--rate-loadgen requires --http-rates')
    rate_lg=a.rate_loadgen.resolve() if a.rate_loadgen else None
    if a.active_room_hz <= 0:p.error('--active-room-hz must be positive')
    if a.fragment_cache_mb is not None and a.fragment_cache_mb < 0:p.error('--fragment-cache-mb must be nonnegative')
    env={k:v for k,v in os.environ.items() if not k.startswith(('CAMPFIRE_','THRUSTER_')) and k!='TLS_DOMAIN'}
    for line in (rust/'parity/.env.reference').read_text().splitlines():
        if line and not line.startswith('#') and '=' in line:
            k,v=line.split('=',1); env[k]=v
    env.update(TMPDIR=str(ROOT/'.cache/tmp'),DISABLE_SSL='true',TARGET_BIND='127.0.0.1',GOMAXPROCS='4',TOKIO_WORKER_THREADS='4',RAILS_MAX_THREADS='4')
    if a.fragment_cache_mb is not None:env['CAMPFIRE_FRAGMENT_CACHE_MB']=str(a.fragment_cache_mb)
    metadata={'time':datetime.datetime.now(datetime.timezone.utc).isoformat(),'host':platform.platform(),'cpu':text('lscpu'),
        'rust_commit':text('git','-C',str(rust),'rev-parse','HEAD'),'settings':{k:str(v) if isinstance(v,Path) else v for k,v in vars(a).items()},
        'binaries':{k:{'sha256':digest(v),'bytes':v.stat().st_size} for k,v in binaries.items()},'seed_sha256':digest(seed/'db/production.sqlite3'),
        'harness_sha256':digest(Path(__file__)), 'form_contract_sha256':digest(ROOT/'bench/message_forms.py'),
        'go_version':text('go','version'),'rust_version':text('rustc','--version') if 'rust' in a.apps else None,'vips_version':text('pkg-config','--modversion','vips'),'go_mod_sha256':digest(ROOT/'go.mod'),'go_sum_sha256':digest(ROOT/'go.sum'),'rust_lock_sha256':digest(rust/'Cargo.lock'),'loadgen_sha256':digest(lg),'rate_loadgen_sha256':digest(rate_lg) if rate_lg else None,
        'rate_loadgen_sources_sha256':{str(f.relative_to(ROOT)):digest(f) for f in sorted((ROOT/'bench/httprate').glob('*.go'))} if rate_lg else None,'load_before':Path('/proc/loadavg').read_text(),
        'go_sources_sha256':{str(f.relative_to(ROOT)):digest(f) for folder in ('cmd','internal','assets','third_party') for f in sorted((ROOT/folder).rglob('*')) if f.is_file()},
        'go_commit':text('git','rev-parse','HEAD'),'gc_environment':env.get('GOGC','unset; application startup may override the Go default'),
        'scope':f'Release binaries; {a.listener} HTTP/1.1 listener; {"gzip" if a.gzip else "identity"} encoding; identical seed and CPU affinity. Media use installed native libraries. Host background load recorded.'}
    results=[]; contracts={}; body_hashes={}; thumbnail_hash=None
    form_messages={}; form_boosts=[]
    if FORM_ROUTES.intersection(a.routes or []):
        with sqlite3.connect(f'file:{seed}/db/production.sqlite3?mode=ro',uri=True) as check:
            for name in FORM_ROUTES.intersection(a.routes):
                label='messages.busy_060' if name=='message_edit' else 'messages.file' if name=='message_edit_attachment' else 'messages.boosted_many'
                form_messages[name]=seed_message(check,labels[label])
            if 'boosts_index' in form_messages:
                form_boosts=check.execute('SELECT b.id,b.booster_id,b.content FROM boosts b JOIN users u ON u.id=b.booster_id WHERE b.message_id=? ORDER BY b.created_at',(form_messages['boosts_index']['id'],)).fetchall()
    if a.resume:
        prior=json.loads((out/'metadata.json').read_text())
        for key in ('binaries','seed_sha256','go_sources_sha256','go_mod_sha256','go_sum_sha256','rust_lock_sha256','loadgen_sha256','harness_sha256','form_contract_sha256','rate_loadgen_sha256','rate_loadgen_sources_sha256','gc_environment'):
            if prior.get(key)!=metadata[key]:raise RuntimeError(f'cannot resume changed {key}')
        for key in prior['settings'].keys() | metadata['settings'].keys():
            if key!='resume' and metadata['settings'].get(key)!=prior['settings'].get(key):raise RuntimeError(f'cannot resume changed setting {key}')
        prior.setdefault('resumptions',[]).append({'time':metadata['time'],'load_before':metadata['load_before']})
        metadata=prior
        results=json.loads((out/'raw.json').read_text())
        for result in results:
            for name,body in result.get('bodies',{}).items():
                value=body['contract'];contracts[name]=value if name in FORM_ROUTES else [v.encode() for v in value] if isinstance(value,list) else value
                if result['app'] in a.same_html_apps and 'origin_normalized_sha256' in body:body_hashes[name]=body['origin_normalized_sha256']
            if 'upload' in result:thumbnail_hash=result['upload']['runs'][0]['thumb_sha256']
    (out/'metadata.json').write_text(json.dumps(metadata,indent=2)+'\n')
    work=ROOT/'bench/.work'; work.mkdir(exist_ok=True)
    for rep in range(1,a.reps+1):
      shift=(rep-1)%len(a.apps)
      order=(a.apps[shift:]+a.apps[:shift]) if len(a.apps)>2 else (a.apps if rep%2 else list(reversed(a.apps)))
      for app in order:
       if any(r['app']==app and r['rep']==rep for r in results):continue
       with tempfile.TemporaryDirectory(prefix=f'{app}-{rep}-',dir=work) as tmp:
        tmp=Path(tmp); db=tmp/'database.sqlite3'
        with sqlite3.connect(f'file:{seed}/db/production.sqlite3?mode=ro',uri=True) as source,sqlite3.connect(db) as dest: source.backup(dest)
        shutil.copytree(seed/'storage',tmp/'storage-root/files')
        target,front=port(),port()
        while target==front:front=port()
        base=f'http://127.0.0.1:{front if a.listener=="public" else target}'
        runenv=env|dict(CAMPFIRE_DATABASE_PATH=str(db),CAMPFIRE_STORAGE_PATH=str(tmp/'storage-root'),CAMPFIRE_FILES_PATH=str(tmp/'storage-root/files'),TARGET_PORT=str(target),HTTP_PORT=str(front))
        if a.cpu_profile:runenv['GO_CPU_PROFILE']=str(out/f'{app}-{rep}.pprof')
        result={'app':app,'rep':rep,'http':[],'cable':[],'load_start':Path('/proc/loadavg').read_text()}
        with (out/f'{app}-{rep}.log').open('w') as log:
         started=time.monotonic(); proc=subprocess.Popen(['taskset','-c',a.server_cpus,str(binaries[app]),'server'],cwd=ROOT,env=runenv,stdout=log,stderr=subprocess.STDOUT)
         try:
          while True:
           if proc.poll() is not None:raise RuntimeError(f'{app} exited; see {log.name}')
           try:
            with urllib.request.urlopen(base+'/up',timeout=1) as response:
             assert response.status==200;break
           except OSError:
            if time.monotonic()-started>30:raise RuntimeError('startup timeout')
            time.sleep(.025)
          result['startup_ms']=round((time.monotonic()-started)*1000,2)
          result['idle_memory_kib']=memory(proc.pid)
          def load(command,**opts):
           fixed=command=='http-rate'
           args=['taskset','-c',a.loadgen_cpus,str(rate_lg if fixed else lg),*([] if fixed else [command]),'--base',base]
           for k,v in opts.items():args+=['--'+k.replace('_','-'),str(v)]
           with (out/f'{app}-{rep}-loadgen.log').open('a') as err:
            value=json.loads(subprocess.check_output(args,text=True,stderr=err,timeout=180))
           if (command=='http' or fixed) and (value['errors'] or set(value['statuses'])!={'200'} or (fixed and value['unscheduled'])):
            (out/f'{app}-{rep}-rejected-http.json').write_text(json.dumps({'command':command,'options':{k:v for k,v in opts.items() if k not in ('cookie','csrf')},'sample':value},indent=2)+'\n')
            raise RuntimeError(f'{command} failed: {value}')
           return value
          cookie=load('login',email=labels['emails.david'],password=labels['passwords.all'])['cookie']
          room=labels['rooms.watercooler']; write_room=labels['rooms.hq']
          scrape=load('scrape',cookie=cookie,room=room)
          if scrape['status']!=200 or len(scrape['streams'])!=3:raise RuntimeError(f'incomplete room: {scrape}')
          result['scrape']=scrape
          routes=[('room_show',f'/rooms/{room}'),('active_room',f'/rooms/{room}'),('messages_page',f'/rooms/{room}/messages?before={labels["messages.busy_060"]}'),('sidebar','/users/me/sidebar'),('search','/searches?q=coffee'),('active_search','/searches?q=coffee'),('avatar',f'/users/{labels["avatar_tokens.jason"]}/avatar'),('static_css',scrape['css']),('post_message',None)]
          for name,m in sorted(form_messages.items()):
           path=f'/rooms/{m["room"]}/messages/{m["id"]}/edit' if name.startswith('message_edit') else f'/messages/{m["id"]}/boosts'+('/new' if name=='new_boost' else '')
           routes.append((name,path))
          if a.routes:routes=[(name,path) for name,path in routes if name in a.routes]
          else:routes=[(name,path) for name,path in routes if name not in ('active_room','active_search') and name not in FORM_ROUTES]
          for name,path in routes:
           opts=dict(cookie=cookie,gzip=a.gzip)
           opts.update(dict(path=path) if path else dict(post_room=write_room,csrf=scrape['csrf'] or ''))
           # Verify representative content before timing, including nonempty message lists.
           if path:
            req=urllib.request.Request(base+path,headers={'Cookie':cookie,'Accept-Encoding':'identity'})
            with urllib.request.urlopen(req) as response:body=response.read()
            # Retain rejected preflight bodies too, not just successful runs.
            (out/f'{app}-{rep}-{name}-identity.html.gz').write_bytes(gzip.compress(body,mtime=0))
            if name in ('room_show','active_room','messages_page') and b'data-message-id=' not in body and b'id="message_' not in body:raise RuntimeError(f'{app} empty {name}')
            result.setdefault('bodies',{})[name]={'bytes':len(body),'sha256':hashlib.sha256(body).hexdigest()}
            encoded_request=urllib.request.Request(base+path,headers={'Cookie':cookie,'Accept-Encoding':'gzip' if a.gzip else 'identity'})
            with urllib.request.urlopen(encoded_request) as encoded_response:
             encoded=encoded_response.read();content_encoding=encoded_response.headers.get('Content-Encoding','identity')
            decoded=gzip.decompress(encoded) if content_encoding=='gzip' else encoded
            def canonical(value):
             # Explicit opt-in only for comparisons containing the original defect.
             # Current candidates must match complete decoded bytes without masking.
             return re.sub(rb'(data-refresh-room-loaded-at-value=")[0-9]+',rb'\g<1>CURSOR',value) if a.mask_legacy_room_cursor and name in ('room_show','active_room') else value
            if canonical(decoded)!=canonical(body):raise RuntimeError(f'{app} {name}: encoded body differs from full identity response')
            result['bodies'][name].update(encoded_bytes=len(encoded),encoding=content_encoding)
            if name=='room_show':(out/f'{app}-{rep}-room.html.gz').write_bytes(gzip.compress(body,mtime=0))
            semantic = sorted(set(re.findall(rb'data-message-id="([0-9]+)"',body))) if name in ('room_show','active_room','messages_page','search','active_search') else sorted(set(re.findall(rb'data-room-id="([0-9]+)"',body))) if name=='sidebar' else hashlib.sha256(body).hexdigest()
            if not semantic:raise RuntimeError(f'empty workload contract: {name}')
            if name in ('search','active_search'):
             counter=re.search(rb'(?s)class="searches__query[^"\n]*".*?<span class="flex-item-no-shrink">([0-9]+)</span>',body)
             if counter is None or int(counter[1])!=len(semantic):raise RuntimeError(f'{app} search result count differs from rendered message IDs')
             result['bodies'][name]['result_count']=int(counter[1])
            if name in FORM_ROUTES:
             semantic=form_contract(name,body,form_messages[name],form_boosts)
             (out/f'{app}-{rep}-{name}.html.gz').write_bytes(gzip.compress(body,mtime=0))
            normalized_hash=hashlib.sha256(body.replace(base.encode(),b'http://benchmark.invalid')).hexdigest()
            result['bodies'][name]['origin_normalized_sha256']=normalized_hash
            if app in a.same_html_apps:
             if name in body_hashes and body_hashes[name]!=normalized_hash:raise RuntimeError(f'{app} {name}: full decoded response differs after listener-origin normalization')
             body_hashes[name]=normalized_hash
            if name in contracts and contracts[name]!=semantic:raise RuntimeError(f'{app} {name} differs from the first application response contract')
            contracts[name]=semantic
            result['bodies'][name]['contract']=semantic if name in FORM_ROUTES else [v.decode() for v in semantic] if isinstance(semantic,list) else semantic
           stop=threading.Event(); writer=None; active_errors=[]; active_posts=[]
           before_writes=None
           if not path:
            with sqlite3.connect(db) as check:before_writes=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(write_room,)).fetchone()[0]
           if name in ('active_room','active_search'):
            with sqlite3.connect(db) as check:
             active_before=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(room,)).fetchone()[0]
             active_indexed_before=check.execute('SELECT count(*) FROM message_search_index WHERE body MATCH ?',('bench active',)).fetchone()[0]
            def mutate():
             try:
              while not stop.is_set():
               started=time.monotonic()
               payload=f'message%5Bbody%5D=bench+active+{"coffee+" if name=="active_search" else ""}{len(active_posts)}'.encode()
               request=urllib.request.Request(base+f'/rooms/{room}/messages',data=payload,headers={'Cookie':cookie,'Content-Type':'application/x-www-form-urlencoded','Accept':'text/vnd.turbo-stream.html','Origin':base,'X-CSRF-Token':scrape['csrf'] or ''})
               with urllib.request.urlopen(request,timeout=10) as response:
                posted=response.read();assert response.status==200 and b'action="append"' in posted
               active_posts.append(1)
               stop.wait(max(0,1/a.active_room_hz-(time.monotonic()-started)))
             except Exception as error:active_errors.append(str(error))
            writer=threading.Thread(target=mutate);writer.start()
           try:
            warmup=load('http',conc=4,duration=2,**opts)
            writes=warmup['ok']
            trials=[('http-rate',a.concurrency[0],{'rate':rate}) for rate in a.http_rates] if a.http_rates else [('http',c,{}) for c in a.concurrency]
            for command,c,rate in trials:
             cpu=cpu_seconds(proc.pid)
             sample=load(command,conc=c,duration=a.seconds,**rate,**opts);sample['route']=name;sample['cpu_us_per_request']=(cpu_seconds(proc.pid)-cpu)*1e6/max(1,sample['ok']);result['http'].append(sample);writes+=sample['ok']
             offered=f' offered={sample["offered_rps"]}' if rate else ''
             print(f'{app} {rep} {name} c={c}{offered}: {sample["rps"]} req/s, p99={sample["latency"].get("p99_ms")}ms',flush=True)
           finally:
            stop.set()
            if writer:writer.join()
           if name in ('active_room','active_search'):
            with sqlite3.connect(db) as check:
             active_saved=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(room,)).fetchone()[0]-active_before
             active_indexed=check.execute('SELECT count(*) FROM message_search_index WHERE body MATCH ?',('bench active',)).fetchone()[0]-active_indexed_before
            if active_errors or active_saved!=len(active_posts) or active_indexed!=len(active_posts):raise RuntimeError(f'active-room write validation failed: {active_errors}, {active_saved}, {active_indexed}, {len(active_posts)}')
            req=urllib.request.Request(base+path,headers={'Cookie':cookie,'Accept-Encoding':'gzip' if a.gzip else 'identity'})
            with urllib.request.urlopen(req) as response:
             changed=response.read()
             if response.headers.get('Content-Encoding')=='gzip':changed=gzip.decompress(changed)
            if b'bench active' not in changed:raise RuntimeError('active room response is stale')
            result['active_search_validation' if name=='active_search' else 'active_validation']={'posts':len(active_posts),'persisted':active_saved,'indexed':active_indexed,'target_hz':a.active_room_hz,'final_plain_bytes':len(changed)}
            if name=='active_search':
             counter=re.search(rb'(?s)class="searches__query[^"\n]*".*?<span class="flex-item-no-shrink">([0-9]+)</span>',changed)
             count=len(set(re.findall(rb'data-message-id="([0-9]+)"',changed)))
             if counter is None or int(counter[1])!=count:raise RuntimeError(f'{app} changed search count differs from rendered message IDs')
             result['active_search_validation']['final_result_count']=count
           if before_writes is not None:
            with sqlite3.connect(db) as check:
             persisted=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(write_room,)).fetchone()[0]-before_writes
             indexed=check.execute('SELECT count(*) FROM message_search_index WHERE body MATCH ?',('bench write',)).fetchone()[0]
            if persisted!=writes or indexed!=writes:raise RuntimeError(f'message/index mismatch: expected {writes}, saved {persisted}, indexed {indexed}')
            result['write_validation']={'expected':writes,'persisted':persisted,'indexed':indexed}
          result['http_memory_kib']=memory(proc.pid)
          for n in a.cable_clients:
           for compression in a.deflate:
            with sqlite3.connect(db) as check:
             cable_before=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(room,)).fetchone()[0]
             cable_indexed_before=check.execute('SELECT count(*) FROM message_search_index WHERE body MATCH ?',('fanout',)).fetchone()[0]
            sample=load('cable',cookie=cookie,room=room,csrf=scrape['csrf'] or '',streams=','.join(scrape['streams']),clients=n,tput_secs=a.cable_seconds,posters=4,deflate=compression)
            with sqlite3.connect(db) as check:
             cable_saved=check.execute('SELECT count(*) FROM messages WHERE room_id=?',(room,)).fetchone()[0]-cable_before
             cable_indexed=check.execute('SELECT count(*) FROM message_search_index WHERE body MATCH ?',('fanout',)).fetchone()[0]-cable_indexed_before
            cable_expected=sample['throughput']['posted']+sample['latency']['messages']
            if cable_saved!=cable_expected or cable_indexed!=cable_expected:raise RuntimeError(f'Cable message/index mismatch: expected {cable_expected}, saved {cable_saved}, indexed {cable_indexed}')
            sample['write_validation']={'expected':cable_expected,'persisted':cable_saved,'indexed':cable_indexed}
            sample['deflate']=bool(compression);result['cable'].append(sample)
            (out/f'{app}-{rep}-partial.json').write_text(json.dumps(result,indent=2)+'\n')
            if sample['ready']!=n or sample['failed'] or sample['throughput']['complete']!=sample['throughput']['posted'] or sample['latency']['complete']!=sample['latency']['messages']:raise RuntimeError(f'incomplete Cable delivery: {sample}')
            print(f'{app} {rep} Cable {n} deflate={compression}: {sample["throughput"]["delivered_msgs_per_sec"]} messages/s',flush=True)
          if a.upload_reps:
           raw=subprocess.check_output(['taskset','-c',a.loadgen_cpus,str(ROOT/'bench/upload'),'--base',base,'--cookie',cookie,'--room',str(write_room),'--file',str(rust/'reference/test/fixtures/files/black_hole.jpg'),'--reps',str(a.upload_reps)],text=True,timeout=180)
           result['upload']=json.loads(raw)
           actual_hash=result['upload']['runs'][0]['thumb_sha256']
           if thumbnail_hash is not None and actual_hash!=thumbnail_hash:raise RuntimeError(f'{app} thumbnail differs from the first application output')
           thumbnail_hash=actual_hash
          result['final_memory_kib']=memory(proc.pid)
          result['load_end']=Path('/proc/loadavg').read_text();results.append(result)
          (out/f'{app}-{rep}.json').write_text(json.dumps(result,indent=2)+'\n')
          (out/'raw.json').write_text(json.dumps(results,indent=2)+'\n')
         finally:
          proc.terminate()
          try:proc.wait(timeout=15)
          except subprocess.TimeoutExpired:proc.kill();proc.wait()
    subprocess.check_call([str(ROOT/'bench/report'),str(out)])
    print(out/'report.md')
if __name__=='__main__':main()
