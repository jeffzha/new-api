"""Restore the recorded private resolver after an independently managed Caddy restart."""
import json
import subprocess
import sys
import time
from pathlib import Path

def run(*args):
    return subprocess.check_output(args,stderr=subprocess.DEVNULL,timeout=10)

def once():
    marker=Path(sys.argv[1])
    if not marker.exists():return
    cfg=json.loads(marker.read_text())
    edge=json.loads(run('docker','inspect',cfg['caddy']))[0]
    dns=json.loads(run('docker','inspect',cfg['dns']))[0]
    if not edge['State']['Running'] or not dns['State']['Running']:return
    ip=dns['NetworkSettings']['Networks'][cfg['network']]['IPAddress']
    assert ip
    body=f'nameserver {ip}\nnameserver 127.0.0.11\nsearch .\noptions edns0 trust-ad ndots:0 timeout:1 attempts:1\n'
    if run('docker','exec',cfg['caddy'],'cat','/etc/resolv.conf')==body.encode():return
    source=marker.parent/'resolv.private'
    source.write_text(body);source.chmod(0o600)
    run('docker','cp',str(source),cfg['caddy']+':/tmp/gateway-resolv.private')
    run('nsenter','-t',str(edge['State']['Pid']),'-m','--root','--wd=/','--','/bin/mount','--bind','/tmp/gateway-resolv.private','/etc/resolv.conf')
    assert run('docker','exec',cfg['caddy'],'cat','/etc/resolv.conf')==body.encode()
    print('Restored Gateway private resolver without reloading Caddy',flush=True)

if __name__=='__main__':
    if '--once' in sys.argv:once()
    else:
        while True:
            try:once()
            except (OSError,subprocess.SubprocessError,KeyError,ValueError,AssertionError) as exc:
                print('Resolver reconciliation deferred: '+type(exc).__name__,flush=True)
            time.sleep(5)
