"""Archive image-bound Agency assets before changing the overseas HTTP slot."""
import argparse
import datetime
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.error
import urllib.request

BEGIN = '# BEGIN OVERSEAS AGENCY RELEASE STATIC ASSETS'
END = '# END OVERSEAS AGENCY RELEASE STATIC ASSETS'


class Entries(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = set()

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        value = attrs.get('src' if tag == 'script' else 'href', '')
        if tag in ('script', 'link') and value.startswith('/agency/assets/'):
            self.paths.add(value)


def static_block(config, root):
    root = str(root)
    if not re.fullmatch(r'/[A-Za-z0-9_/-]+', root) or '..' in root.split('/'):
        raise ValueError('Unsafe Agency archive root')
    block = f'''{BEGIN}
location ^~ /agency/assets/ {{
    root {root};
    try_files $uri =404;
    add_header Cache-Control "public, max-age=31536000, immutable";
    add_header X-Content-Type-Options nosniff always;
}}
{END}

'''
    if BEGIN in config or END in config:
        pattern = r'(?ms)^' + re.escape(BEGIN) + r'.*?^' + re.escape(END) + r'\n\n'
        matches = list(re.finditer(pattern, config))
        if len(matches) != 1:
            raise ValueError('Expected one managed Agency archive block')
        match = matches[0]
        return config[:match.start()] + block + config[match.end():]
    anchor = 'location ^~ /agency/ {'
    if config.count(anchor) != 1 or '/agency/assets/' in config:
        raise ValueError('Expected one Agency proxy and no asset route')
    return config.replace(anchor, block + anchor, 1)


def get(url):
    with urllib.request.urlopen(url, timeout=20) as response:
        return response.headers.get_content_type(), response.read()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def verify(url, expected):
    mime, body = get(url)
    allowed = {'application/javascript', 'text/javascript'} if url.endswith('.js') else {'text/css'}
    if mime not in allowed or body.lstrip().lower().startswith((b'<html', b'<!doctype')):
        raise ValueError('Invalid Agency asset MIME: ' + url)
    if digest(body) != expected:
        raise ValueError('Agency asset checksum mismatch: ' + url)
    return body


def image_assets(container, base):
    image = subprocess.check_output(['docker', 'inspect', '-f', '{{.Image}}', container], text=True).strip()
    with tempfile.TemporaryDirectory() as directory:
        binary = Path(directory) / 'agency-hub'
        subprocess.run(['docker', 'cp', container + ':/agency-hub', str(binary)], check=True, capture_output=True)
        raw = binary.read_bytes()
    # Go embed retains the full asset paths in the executable's file table.
    names = sorted(set(name.decode() for name in re.findall(rb'webdist/assets/(index-[a-f0-9]{8}\.(?:js|css))', raw)))
    if not names:
        raise ValueError('No embedded Agency asset inventory')
    parser = Entries()
    parser.feed(get(base + '/agency/')[1].decode())
    paths = {'/agency/assets/' + name for name in names}
    if not parser.paths or not parser.paths <= paths:
        raise ValueError('Agency HTML does not match embedded inventory')
    files = {}
    bodies = {}
    for path in sorted(paths):
        mime, body = get(base + path)
        expected = digest(body)
        verify(base + path, expected)
        files[path], bodies[path] = expected, body
    return {'image': image, 'binary_sha256': digest(raw), 'entries': sorted(parser.paths), 'files': files}, bodies


def publish(args):
    import fcntl
    os.umask(0o022)
    with open(args.config.parent / '.overseas-blue-green.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        original = args.config.read_bytes()
        candidate = static_block(original.decode(), args.root).encode()
        subprocess.run(['nginx', '-t'], check=True, capture_output=True)
        manifests, known, bodies = [], {}, {}
        for container, base in args.slot:
            manifest, content = image_assets(container, base)
            for path, expected in manifest['files'].items():
                destination = args.root / path.lstrip('/')
                if (path in known and known[path] != expected) or (destination.exists() and digest(destination.read_bytes()) != expected):
                    raise ValueError('Refusing to overwrite Agency asset: ' + path)
                known[path], bodies[path] = expected, content[path]
            manifests.append((container, manifest))
        for path, body in bodies.items():
            destination = args.root / path.lstrip('/')
            destination.parent.mkdir(parents=True, exist_ok=True)
            if not destination.exists():
                with destination.open('xb') as stream:
                    stream.write(body)
        if args.config.read_bytes() != original:
            raise ValueError('Agency Nginx configuration changed concurrently')
        args.backup.mkdir(parents=True, exist_ok=True, mode=0o700)
        stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
        (args.backup / ('agency-assets-before-' + stamp + '.conf')).write_bytes(original)
        try:
            args.config.write_bytes(candidate)
            subprocess.run(['nginx', '-t'], check=True, capture_output=True)
            subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
            for path, expected in known.items():
                verify(args.public_url.rstrip('/') + path, expected)
            try:
                get(args.public_url.rstrip('/') + '/agency/assets/missing-release-probe.js')
                raise ValueError('Missing Agency asset returned success')
            except urllib.error.HTTPError as error:
                if error.code != 404:
                    raise
            records = args.root / 'releases'
            records.mkdir(exist_ok=True)
            for container, manifest in manifests:
                image = subprocess.check_output(['docker', 'inspect', '-f', '{{.Image}}', container], text=True).strip()
                if image != manifest['image']:
                    raise ValueError('Agency container changed during publication')
                (records / (image.replace(':', '-') + '.json')).write_text(json.dumps(manifest, sort_keys=True))
        except BaseException:
            args.config.write_bytes(original)
            subprocess.run(['nginx', '-t'], check=True, capture_output=True)
            subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
            raise
        print(json.dumps({'archived_assets': len(known), 'slots': len(manifests), 'public_mime_sha_verified': True, 'application_traffic_unchanged': True}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--slot', nargs=2, action='append', required=True, metavar=('CONTAINER', 'BASE_URL'))
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--backup', type=Path, required=True)
    parser.add_argument('--public-url', required=True)
    publish(parser.parse_args())
