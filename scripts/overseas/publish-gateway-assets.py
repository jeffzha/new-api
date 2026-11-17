"""Archive exact overseas image assets with a validated graceful Nginx reload."""
import argparse
import datetime
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
import tempfile

spec = importlib.util.spec_from_file_location('gateway_assets', Path(__file__).resolve().parents[1] / 'gateway-static-assets.py')
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)
BEGIN = '# BEGIN OVERSEAS GATEWAY RELEASE STATIC ASSETS'
END = '# END OVERSEAS GATEWAY RELEASE STATIC ASSETS'


def static_block(config, root):
    root = str(root)
    if not re.fullmatch(r'/[A-Za-z0-9_/-]+', root) or '..' in root.split('/'):
        raise ValueError('Unsafe static archive root')
    block = f'''    {BEGIN}
    location ^~ /static/ {{
        root {root};
        try_files $uri =404;
        access_log off;
        add_header Cache-Control "public, max-age=31536000, immutable" always;
    }}
    {END}

'''
    if BEGIN in config or END in config:
        pattern = r'(?ms)^    ' + re.escape(BEGIN) + r'.*?^    ' + re.escape(END) + chr(10) * 2
        matches = list(re.finditer(pattern, config))
        if len(matches) != 1:
            raise ValueError('Expected one managed overseas static block')
        match = matches[0]
        return config[:match.start()] + block + config[match.end():]
    anchor = '    location ^~ /static/llm-brand/ {'
    if config.count(anchor) != 1 or re.search(r'location[^' + chr(10) + r']* /static/ *[{]', config):
        raise ValueError('Expected one overseas branding route and no general static route')
    return config.replace(anchor, block + anchor, 1)


def stage_bundle(bundle, scratch):
    with tarfile.open(bundle, 'r:gz') as archive:
        members = archive.getmembers()
        names = [member.name for member in members]
        if len(names) != len(set(names)):
            raise ValueError('Duplicate archive members')
        for member in members:
            if not member.isfile() or (member.name != 'manifest.json' and assets.asset_path('/' + member.name) != member.name):
                raise ValueError('Invalid archive member')
        archive.extractall(scratch, filter='data')
    manifest = json.loads((scratch / 'manifest.json').read_text())
    if set(names) != {'manifest.json', *(assets.asset_path(name) for name in manifest['files'])}:
        raise ValueError('Archive does not match manifest')
    if not manifest['entries'] or not set(manifest['entries']) <= manifest['files'].keys():
        raise ValueError('Invalid manifest entry assets')
    for name, expected in manifest['files'].items():
        if assets.digest((scratch / assets.asset_path(name)).read_bytes()) != expected:
            raise ValueError('Corrupt bundle asset: ' + name)
    return manifest


def publish(args):
    import fcntl
    os.umask(0o022)
    args.root.mkdir(parents=True, exist_ok=True)
    with open(args.config.parent / '.overseas-blue-green.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        original = args.config.read_bytes()
        candidate = static_block(original.decode(), args.root).encode()
        assets.run('nginx', '-t')
        with tempfile.TemporaryDirectory(prefix='.publish-', dir=args.root) as temporary:
            staged, known = [], {}
            for index, (bundle, container) in enumerate(args.bundle):
                scratch = Path(temporary) / str(index)
                scratch.mkdir()
                manifest = stage_bundle(bundle, scratch)
                html = assets.run('docker', 'exec', container, 'wget', '-qO-', 'http://127.0.0.1:3000/').decode()
                if assets.entries(html) != manifest['entries']:
                    raise ValueError('Bundle does not match running container: ' + container)
                image = assets.run('docker', 'inspect', '-f', '{{.Image}}', container).decode().strip()
                network = json.loads(assets.run('docker', 'inspect', '-f', '{{json .NetworkSettings.Networks}}', container))
                address = next(value['IPAddress'] for value in network.values() if value.get('IPAddress'))
                for name in manifest['entries']:
                    assets.get_asset('http://' + address + ':3000' + name, manifest['files'][name])
                for name, expected in manifest['files'].items():
                    destination = args.root / assets.asset_path(name)
                    if (name in known and known[name] != expected) or (destination.exists() and assets.digest(destination.read_bytes()) != expected):
                        raise ValueError('Refusing to overwrite published asset: ' + name)
                    known[name] = expected
                staged.append((scratch, manifest, container, image))
            for scratch, manifest, _, _ in staged:
                for name in manifest['files']:
                    destination = args.root / assets.asset_path(name)
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    if not destination.exists():
                        source = scratch / assets.asset_path(name)
                        source.chmod(0o644)
                        os.link(source, destination)
            if args.config.read_bytes() != original:
                raise ValueError('Nginx configuration changed concurrently')
            backup = args.backup / ('static-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
            backup.mkdir(parents=True, mode=0o700)
            shutil.copy2(args.config, backup / 'site.before.conf')
            try:
                args.config.write_bytes(candidate)
                assets.run('nginx', '-t')
                assets.run('systemctl', 'reload', 'nginx')
                for _, manifest, container, image in staged:
                    assets.verify(manifest, args.public_url)
                    if assets.run('docker', 'inspect', '-f', '{{.Image}}', container).decode().strip() != image:
                        raise ValueError('Container changed during publication')
                    records = args.root / 'releases'
                    records.mkdir(exist_ok=True)
                    (records / (image.replace(':', '-') + '.json')).write_text(json.dumps(manifest, sort_keys=True))
            except Exception:
                args.config.write_bytes(original)
                assets.run('nginx', '-t')
                assets.run('systemctl', 'reload', 'nginx')
                raise
            print('Old and new static assets verified; application traffic unchanged. Backup: ' + str(backup))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', nargs=2, action='append', required=True, metavar=('ARCHIVE', 'CONTAINER'))
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--backup', type=Path, required=True)
    parser.add_argument('--public-url', required=True)
    publish(parser.parse_args())
