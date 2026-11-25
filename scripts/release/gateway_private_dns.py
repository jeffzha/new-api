"""Inspect or change an already provisioned private DNS Gateway traffic pool.

This command never installs DNS, replaces application containers, or reloads
Caddy. The release marker binds container identities, the unchanged ingress
configuration, and candidate preparation receipts to the selected environment.
"""
import argparse
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import tempfile


def docker(*args):
    return subprocess.check_output(['docker', *args], stderr=subprocess.DEVNULL, timeout=15)


def inspect(name):
    return json.loads(docker('inspect', name))[0]


def atomic(path, text):
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as stream:
        temporary = Path(stream.name)
        try:
            os.fchmod(stream.fileno(), 0o644)
            stream.write(text.encode())
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def prepared(marker, node, candidate):
    if node['State'].get('Health', {}).get('Status') != 'healthy':
        raise ValueError('Target container has not passed its health check')
    if node['Id'] != candidate['containerId'] or node['Image'] != candidate['imageId']:
        raise ValueError('Prepared target identity changed; run release gates again')
    for kind in ('assets', 'indexes'):
        receipt = json.loads(Path(candidate[kind + 'Receipt']).read_text())
        if receipt.get('ready') is not True:
            raise ValueError(kind + ' preparation is not ready')
    address = node['NetworkSettings']['Networks'][marker['network']]['IPAddress']
    return str(ipaddress.ip_address(address))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--marker', type=Path, required=True)
    operations = parser.add_subparsers(dest='operation', required=True)
    operations.add_parser('status')
    operations.add_parser('rollback')
    split = operations.add_parser('split')
    split.add_argument('--green-percent', type=int, choices=range(101), required=True)
    args = parser.parse_args()
    marker = json.loads(args.marker.read_text())
    if marker['mode'] != 'private-dns':
        raise ValueError('Environment does not use private DNS routing')
    deploy = args.marker.parent.parent
    with (deploy / '.gateway-blue-green.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        caddy = inspect(marker['caddy'])
        if caddy['Id'] != marker['caddyId']:
            raise ValueError('Caddy identity changed; confirm its new active configuration first')
        active = docker('exec', marker['caddy'], 'curl', '-fsS', '--max-time', '5',
                        'http://127.0.0.1:2019/config/')
        if hashlib.sha256(active.strip()).hexdigest() != marker['caddyConfigSha256']:
            raise ValueError('Ingress configuration changed; reconcile routing before continuing')
        root = Path(marker['dnsConfig'])
        if args.operation == 'status':
            print(json.dumps({'mode': marker['mode'], 'blue': marker['blue'],
                              'green': marker['green'], 'hosts': (root / 'hosts').read_text(),
                              'weights': (root / 'weights').read_text(), 'caddyUnchanged': True}, indent=2))
            return
        if not inspect(marker['dns'])['State']['Running']:
            raise ValueError('Private resolver is not running')
        if args.operation == 'rollback':
            fallback = inspect(marker['fallback'])
            if fallback['State'].get('Health', {}).get('Status') != 'healthy':
                raise ValueError('Recorded fallback is not healthy')
            ip = str(ipaddress.ip_address(fallback['NetworkSettings']['Networks'][marker['network']]['IPAddress']))
            atomic(root / 'hosts', ip + ' new-api\n' + ip + ' new-api-green\n')
        else:
            addresses = {}
            for color, share in (('blue', 100 - args.green_percent), ('green', args.green_percent)):
                if share:
                    addresses[color] = (prepared(marker, inspect(marker[color]), marker['prepared'][color]), share)
            # Update weights first, then atomically expose only prepared positive-weight targets.
            atomic(root / 'weights', 'new-api\n' + ''.join(f'{ip} {share}\n' for ip, share in addresses.values()))
            alternate = addresses.get('green', addresses.get('blue'))[0]
            atomic(root / 'hosts', ''.join(ip + ' new-api\n' for ip, _ in addresses.values()) + alternate + ' new-api-green\n')
        print('Private DNS updated; public listeners and existing connections unchanged. Observe rollout gates before proceeding.')


if __name__ == '__main__':
    main()
