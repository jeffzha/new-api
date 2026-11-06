"""Publish immutable build assets before canary traffic; never accept SPA HTML as JS.

Run pack on the build host against the exact dist embedded in the image. Run
publish/verify on the deployment host. The existing api-docs file server serves
the archive; neither application slots nor Caddy need restarting.
"""
import argparse
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import urllib.error
import urllib.request


BEGIN = '# BEGIN GATEWAY RELEASE STATIC ASSETS'
END = '# END GATEWAY RELEASE STATIC ASSETS'


class Entries(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = set()

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        value = attrs.get('src' if tag == 'script' else 'href', '')
        if tag in ('script', 'link') and value.startswith('/static/'):
            self.paths.add(value)


def entries(html):
    parser = Entries()
    parser.feed(html)
    if not any(path.endswith('.js') for path in parser.paths):
        raise ValueError('Homepage has no static JavaScript entry')
    return sorted(parser.paths)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def asset_path(path):
    if not re.fullmatch(r'/static/[A-Za-z0-9_./-]+', path) or '..' in path.split('/'):
        raise ValueError('Unsafe asset path: ' + path)
    return path.lstrip('/')


def pack(dist, output, release):
    files = {}
    for path in sorted((dist / 'static').rglob('*')):
        if path.is_symlink():
            raise ValueError('Symlink in dist: ' + str(path))
        if path.is_file():
            name = '/' + path.relative_to(dist).as_posix()
            asset_path(name)
            files[name] = digest(path.read_bytes())
    refs = entries((dist / 'index.html').read_text(encoding='utf-8'))
    if not files or not set(refs) <= files.keys():
        raise ValueError('Build is incomplete: missing entry assets')
    manifest = {'release': release, 'entries': refs, 'files': files}
    with tarfile.open(output, 'w:gz') as archive:
        for name in files:
            archive.add(dist / asset_path(name), arcname=asset_path(name), recursive=False)
        data = json.dumps(manifest, sort_keys=True).encode()
        import io
        info = tarfile.TarInfo('manifest.json')
        info.size = len(data)
        archive.addfile(info, io.BytesIO(data))
    return manifest


def static_block(config, archive_upstream):
    # Retain the historical archive route, but remove the manually maintained
    # current-build whitelist. New builds must never depend on slot selection.
    pattern = r'(?ms)^[ \t]*# BEGIN (?:PRICING|GATEWAY) RELEASE STATIC ASSETS[^\n]*\n.*?^[ \t]*# END (?:PRICING|GATEWAY) RELEASE STATIC ASSETS[^\n]*'
    matches = list(re.finditer(pattern, config))
    if len(matches) != 1:
        raise ValueError('Expected exactly one managed static block')
    previous = matches[0].group()
    legacy = re.search(r'(?ms)^ +@pricing_legacy_assets path [^\n]+\n +handle @pricing_legacy_assets \{.*?^ +\}', previous)
    legacy_text = legacy.group() + '\n' if legacy else ''
    block = f'''    {BEGIN}
{legacy_text}    handle /static/* {{
        route {{
            rewrite * /gateway-assets{{uri}}
            reverse_proxy {archive_upstream} {{
                @archive_missing status 404
                handle_response @archive_missing {{
                    uri strip_prefix /gateway-assets
                    reverse_proxy new-api-pricing-reserve:3000 {{
                        @legacy_html header Content-Type text/html*
                        handle_response @legacy_html {{
                            respond "Static asset not found" 404
                        }}
                    }}
                }}
            }}
        }}
    }}
    {END}'''
    return config[:matches[0].start()] + block + config[matches[0].end():]


def run(*args, input=None):
    return subprocess.check_output(args, input=input)


def get_asset(url, expected):
    try:
        with urllib.request.urlopen(url, timeout=20) as response:
            content_type = response.headers.get_content_type()
            body = response.read()
    except urllib.error.HTTPError as error:
        raise ValueError(f'Asset HTTP {error.code}: {url}') from error
    extension = url.split('?', 1)[0].rsplit('.', 1)[-1]
    allowed = {'js': {'text/javascript', 'application/javascript'}, 'css': {'text/css'}}
    if content_type == 'text/html' or body.lstrip().lower().startswith((b'<!doctype html', b'<html')):
        raise ValueError('Asset returned HTML: ' + url)
    if extension in allowed and content_type not in allowed[extension]:
        raise ValueError('Wrong asset Content-Type: ' + url + ' ' + content_type)
    if digest(body) != expected:
        raise ValueError('Asset checksum mismatch: ' + url)


def verify(manifest, base_url):
    # Sequential reads keep production CPU/connection load bounded. Include all
    # async chunks, styles, fonts and licenses, not only the HTML's entry script.
    for name, expected in manifest['files'].items():
        get_asset(base_url.rstrip('/') + name, expected)
    print(f"Verified {len(manifest['files'])} assets for {manifest['release']}", flush=True)


def verify_slot(args):
    image = run('docker', 'inspect', '-f', '{{.Image}}', args.container).decode().strip()
    record = args.root / 'releases' / (image.replace(':', '-') + '.json')
    manifest = json.loads(record.read_text())
    html = run('docker', 'exec', args.container, 'wget', '-qO-', 'http://127.0.0.1:3000/').decode()
    if entries(html) != manifest['entries']:
        raise ValueError('Candidate HTML does not match its archived build')
    verify(manifest, args.public_url)


def publish(args):
    import fcntl
    os.umask(0o022)
    args.root.mkdir(parents=True, exist_ok=True)
    # Same lock used by both blue/green deployment scripts.
    with open(args.caddyfile.parent / '.gateway-blue-green.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        original = args.caddyfile.read_bytes()
        with tempfile.TemporaryDirectory(prefix='.gateway-assets-', dir=args.root) as scratch:
            scratch = Path(scratch)
            with tarfile.open(args.bundle, 'r:gz') as archive:
                members = archive.getmembers()
                names = [member.name for member in members]
                if len(names) != len(set(names)):
                    raise ValueError('Duplicate archive members')
                for member in members:
                    if not member.isfile() or (member.name != 'manifest.json' and asset_path('/' + member.name) != member.name):
                        raise ValueError('Invalid archive member: ' + member.name)
                archive.extractall(scratch, filter='data')
            manifest = json.loads((scratch / 'manifest.json').read_text())
            if set(names) != {'manifest.json', *(asset_path(name) for name in manifest['files'])}:
                raise ValueError('Archive does not match manifest')
            for name, expected in manifest['files'].items():
                source = scratch / asset_path(name)
                destination = args.root / asset_path(name)
                if digest(source.read_bytes()) != expected:
                    raise ValueError('Corrupt bundle asset: ' + name)
                if destination.exists() and digest(destination.read_bytes()) != expected:
                    raise ValueError('Refusing to overwrite a previously published asset: ' + name)
            html = run('docker', 'exec', args.container, 'wget', '-qO-', 'http://127.0.0.1:3000/').decode()
            if entries(html) != manifest['entries']:
                raise ValueError('Bundle does not match the running candidate')
            image = run('docker', 'inspect', '-f', '{{.Image}}', args.container).decode().strip()
            # Verify the bytes actually embedded in the image, not a separately
            # rebuilt dist that merely happens to have the same entry names.
            networks = json.loads(run('docker', 'inspect', '-f', '{{json .NetworkSettings.Networks}}', args.container))
            address = next(network['IPAddress'] for network in networks.values() if network.get('IPAddress'))
            verify(manifest, 'http://' + address + ':3000')
            for name in manifest['files']:
                destination = args.root / asset_path(name)
                destination.parent.mkdir(parents=True, exist_ok=True)
                # Hard links make each immutable file visible atomically.
                if not destination.exists():
                    (scratch / asset_path(name)).chmod(0o644)
                    os.link(scratch / asset_path(name), destination)
            candidate = static_block(original.decode(), args.archive_upstream).encode()
            # Validate before writing the host Caddyfile. Preserve its bind inode.
            run('docker', 'exec', '-i', args.caddy_container, 'sh', '-c', 'cat > /tmp/Caddyfile.static.load', input=candidate)
            run('docker', 'exec', args.caddy_container, 'caddy', 'validate', '--config', '/tmp/Caddyfile.static.load', '--adapter', 'caddyfile')
            if args.caddyfile.read_bytes() != original:
                raise ValueError('Caddyfile changed concurrently; retry after review')
            import datetime
            backup = args.caddyfile.parent / 'backups' / ('gateway-assets-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
            backup.mkdir(mode=0o700, parents=True)
            (backup / 'Caddyfile.before').write_bytes(original)
            (backup / 'Caddyfile.before').chmod(0o600)
            try:
                args.caddyfile.write_bytes(candidate)
                run('docker', 'exec', args.caddy_container, 'caddy', 'reload', '--config', '/tmp/Caddyfile.static.load', '--adapter', 'caddyfile')
                verify(manifest, args.public_url)
                current_image = run('docker', 'inspect', '-f', '{{.Image}}', args.container).decode().strip()
                if current_image != image:
                    raise ValueError('Candidate container changed during static publication')
                records = args.root / 'releases'
                records.mkdir(exist_ok=True)
                record = records / (image.replace(':', '-') + '.json')
                temporary = scratch / 'record.json'
                temporary.write_text(json.dumps(manifest, sort_keys=True))
                os.replace(temporary, record)
            except Exception:
                args.caddyfile.write_bytes(original)
                run('docker', 'exec', '-i', args.caddy_container, 'sh', '-c', 'cat > /tmp/Caddyfile.static.rollback', input=original)
                run('docker', 'exec', args.caddy_container, 'caddy', 'reload', '--config', '/tmp/Caddyfile.static.rollback', '--adapter', 'caddyfile')
                raise
            print('Static archive ready; traffic weights unchanged. Backup: ' + str(backup))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    build = commands.add_parser('pack')
    build.add_argument('--dist', type=Path, required=True)
    build.add_argument('--output', type=Path, required=True)
    build.add_argument('--release', required=True)
    for command in ('publish', 'verify'):
        sub = commands.add_parser(command)
        sub.add_argument('--root', type=Path, required=True)
        sub.add_argument('--container', required=True)
        sub.add_argument('--public-url', required=True)
        if command == 'publish':
            sub.add_argument('--bundle', type=Path, required=True)
            sub.add_argument('--caddyfile', type=Path, required=True)
            sub.add_argument('--caddy-container', required=True)
            sub.add_argument('--archive-upstream', default='api-docs:80')
    args = parser.parse_args()
    if args.command == 'pack':
        manifest = pack(args.dist, args.output, args.release)
        print(f"Packed {len(manifest['files'])} assets")
    elif args.command == 'publish':
        publish(args)
    else:
        verify_slot(args)


if __name__ == '__main__':
    main()
