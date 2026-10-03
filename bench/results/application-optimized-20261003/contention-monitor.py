import datetime,gzip,json,os,signal,subprocess,time
from pathlib import Path
out=Path('bench/results/application-optimized-20261003')
events=json.loads((out/'contention-events.json').read_text())
def builds():
 result=[]
 for p in Path('/proc').iterdir():
  if not p.name.isdigit():continue
  try:
   name=(p/'comm').read_text().strip()
   if name in ('clang++','clang-tidy','cc1plus','rustc','ld.lld','ld'):result.append((int(p.name),name))
  except OSError:pass
 return result if len(result)>=4 else []
def note(kind,active):
 events.append(dict(time=datetime.datetime.now(datetime.timezone.utc).isoformat(),event=kind,processes=active))
 (out/'contention-events.json').write_text(json.dumps(events,indent=2)+'\n')
 print(kind,active,flush=True)
def archive():
 completed={(r['app'],r['rep']) for r in json.loads((out/'raw.json').read_text())}
 for app in ('go','rust'):
  for rep in (1,2,3):
   if (app,rep) in completed:continue
   for log in out.glob(f'{app}-{rep}*.log'):
    with gzip.open(out/f'interrupted-{int(time.time())}-{log.name}.gz','wb') as dest:dest.write(log.read_bytes())
archive()
while True:
 quiet=0
 note('waiting for 20 seconds without parallel external compilation (four or more compiler processes)',builds())
 while quiet<20:
  quiet=0 if builds() else quiet+1
  time.sleep(1)
 note('resuming unchanged benchmark',[])
 proc=subprocess.Popen(['bench/application','--out',str(out),'--reps','3','--seconds','5','--resume'])
 interrupted=False
 while proc.poll() is None:
  active=builds()
  if active:
   note('interrupting benchmark due to external compilation',active)
   proc.send_signal(signal.SIGINT);proc.wait();archive();interrupted=True;break
  time.sleep(1)
 if not interrupted:raise SystemExit(proc.returncode)
