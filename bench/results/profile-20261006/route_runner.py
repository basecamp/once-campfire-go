#!/usr/bin/env python3
"""bench/profile equivalent for routes it does not cover (messages_page) and
optional perf-stat attach on the application process. Same seed, env, affinity
(8-11 app / 12-15 loadgen), concurrency 16 and identity encoding."""
import argparse, importlib.machinery, importlib.util, json, os, shutil, sqlite3, subprocess, tempfile
from pathlib import Path
ROOT = Path(__file__).resolve().parents[3]
loader = importlib.machinery.SourceFileLoader('upgrade', str(ROOT / 'bin/check-upgrade'))
spec = importlib.util.spec_from_loader(loader.name, loader)
u = importlib.util.module_from_spec(spec)
loader.exec_module(u)
p = argparse.ArgumentParser()
p.add_argument('route', choices=['room', 'messages', 'sidebar', 'search', 'write'])
p.add_argument('--out', type=Path, required=True)
p.add_argument('--seconds', type=int, default=10)
p.add_argument('--perf', type=int, default=0, help='attach perf stat to the server for this many seconds')
a = p.parse_args()
a.out.mkdir(parents=True, exist_ok=True)
rust = ROOT.parent / 'once-campfire-rust'
seed = rust / 'parity/.seed/default'
labels = json.load(open(seed / 'labels.json'))
env = os.environ.copy()
for line in (rust / 'parity/.env.reference').read_text().splitlines():
    if line and not line.startswith('#') and '=' in line:
        k, v = line.split('=', 1)
        env[k] = v
env.update(DISABLE_SSL='1', TARGET_BIND='127.0.0.1', GOMAXPROCS='4',
           TMPDIR=str(ROOT / '.cache/tmp'), GO_CPU_PROFILE=str((a.out / (a.route + '.pprof')).resolve()))
os.sched_setaffinity(0, {8, 9, 10, 11})
with tempfile.TemporaryDirectory(dir=ROOT / '.cache/tmp', prefix='profile-') as tmp:
    work = Path(tmp)
    db = work / 'db.sqlite3'
    with sqlite3.connect(seed / 'db/production.sqlite3') as src, sqlite3.connect(db) as dst:
        src.backup(dst)
    shutil.copytree(seed / 'storage', work / 'storage/files')
    env.update(CAMPFIRE_DATABASE_PATH=str(db), CAMPFIRE_STORAGE_PATH=str(work / 'storage'),
               CAMPFIRE_FILES_PATH=str(work / 'storage/files'))
    with u.server(ROOT / 'campfire', env, work, 'profile') as base:
        cookie = u.login(base, labels)
        room = labels['rooms.watercooler']
        lg = ['taskset', '-c', '12-15', str(rust / 'target/bench/release/loadgen')]
        path = {'room': f'/rooms/{room}',
                'messages': f'/rooms/{room}/messages?before={labels["messages.busy_060"]}',
                'sidebar': '/users/me/sidebar',
                'search': '/searches?q=coffee'}.get(a.route)
        if a.route == 'write':
            cmd = ['http', '--conc', '16', '--duration', str(a.seconds), '--gzip', '0',
                   '--post-room', str(labels['rooms.hq'])]
        else:
            cmd = ['http', '--conc', '16', '--duration', str(a.seconds), '--gzip', '0', '--path', path]
        if a.perf:
            # campfire is the direct child of this process; u.server does not expose the pid.
            pid = subprocess.check_output(['pgrep', '-P', str(os.getpid()), 'campfire'], text=True).split()[-1]
        result = subprocess.Popen(lg + cmd + ['--base', base, '--cookie', cookie], stdout=subprocess.PIPE, text=True)
        perfout = None
        if a.perf:
            perf = subprocess.Popen(['perf', 'stat', '-p', pid, '-e', 'instructions:u,cycles:u',
                                     'sleep', str(a.perf)], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        out, _ = result.communicate(timeout=120)
        if a.perf:
            _, perr = perf.communicate(timeout=30)
            perfout = perr.decode()
            (a.out / (a.route + '.perfstat')).write_text(perfout)
        (a.out / (a.route + '.json')).write_text(out)
        print(out)