"""Regression cases for hashed release assets and false HTTP-200 health."""
import importlib.util
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
from pathlib import Path
import tarfile
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('assets', Path(__file__).with_name('gateway-static-assets.py'))
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)

spec = importlib.util.spec_from_file_location('overseas_assets', Path(__file__).parent / 'overseas/publish-gateway-assets.py')
overseas_assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(overseas_assets)

spec = importlib.util.spec_from_file_location('agency_assets', Path(__file__).parent / 'overseas/publish-agency-assets.py')
agency_assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(agency_assets)


class StaticAssetsTests(unittest.TestCase):
    def test_agency_archive_preserves_proxy_mock_and_missing_asset_404(self):
        original = '''location = /agency { return 301 /agency/; }
location ^~ /agency/ { proxy_pass http://127.0.0.1:3201; }
location ^~ /model-mock/ { proxy_pass http://127.0.0.1:4180/; }
'''
        updated = agency_assets.static_block(original, '/var/www/llm-static/agency-archive')
        self.assertIn('try_files $uri =404;', updated)
        self.assertIn('root /var/www/llm-static/agency-archive;', updated)
        self.assertTrue(updated.endswith(original[original.index('location ^~ /agency/ {'):]))
        self.assertEqual(updated, agency_assets.static_block(updated, '/var/www/llm-static/agency-archive'))
        for config in [original + original, original.replace('/agency/ {', '/other/ {')]:
            with self.assertRaises(ValueError):
                agency_assets.static_block(config, '/var/www/archive')
        with self.assertRaises(ValueError):
            agency_assets.static_block(original, '/var/www/../secret')

    def test_agency_publication_retains_assets_and_rolls_back_on_corruption(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = root / 'agency.conf'
            original = b'location ^~ /agency/ { proxy_pass http://127.0.0.1:3201; }'
            config.write_bytes(original)
            archive = root / 'archive'
            old = archive / 'agency/assets/index-old.js'
            old.parent.mkdir(parents=True)
            old.write_bytes(b'old()')
            path, body = '/agency/assets/index-new.js', b'new()'
            manifest = {'image': 'sha256:image', 'entries': [path], 'files': {path: assets.digest(body)}}
            args = SimpleNamespace(root=archive, config=config, backup=root / 'backups',
                                   slot=[('candidate', 'http://candidate')], public_url='https://example.test')
            locks = SimpleNamespace(LOCK_EX=1, LOCK_NB=2, flock=lambda *args: None)
            with patch.dict('sys.modules', {'fcntl': locks}), patch.object(agency_assets, 'static_block', return_value='updated'), patch.object(agency_assets, 'image_assets', return_value=(manifest, {path: body})), patch.object(agency_assets.subprocess, 'run') as commands, patch.object(agency_assets.subprocess, 'check_output', return_value='sha256:image'):
                with patch.object(agency_assets, 'verify', side_effect=ValueError('checksum')):
                    with self.assertRaisesRegex(ValueError, 'checksum'):
                        agency_assets.publish(args)
                self.assertEqual(config.read_bytes(), original)
                self.assertEqual(sum(call.args[0] == ['systemctl', 'reload', 'nginx'] for call in commands.call_args_list), 2)
                missing = agency_assets.urllib.error.HTTPError('missing', 404, 'missing', {}, None)
                with patch.object(agency_assets, 'verify'), patch.object(agency_assets, 'get', side_effect=missing):
                    agency_assets.publish(args)
                self.assertEqual(config.read_text(), 'updated')
                self.assertEqual(old.read_bytes(), b'old()')
                self.assertEqual((archive / path.lstrip('/')).read_bytes(), body)
                manifest['files'][path] = assets.digest(b'changed()')
                with self.assertRaisesRegex(ValueError, 'overwrite'):
                    agency_assets.publish(args)
                self.assertEqual(config.read_text(), 'updated')

    def test_overseas_nginx_preserves_routes_and_has_no_spa_asset_fallback(self):
        original = '''server {
    server_name llm.ai.nexus-reach.com;
    location ^~ /static/llm-brand/ { alias /var/www/brand/; }
    include /etc/nginx/snippets/agency-model-mock.conf;
    location / { proxy_pass http://new_api_backend; }
}
'''
        updated = overseas_assets.static_block(original, '/var/www/llm-static/gateway-archive')
        self.assertIn('try_files $uri =404;', updated)
        self.assertIn('location ^~ /static/llm-brand/ { alias /var/www/brand/; }', updated)
        self.assertIn('include /etc/nginx/snippets/agency-model-mock.conf;', updated)
        self.assertIn('location / { proxy_pass http://new_api_backend; }', updated)
        self.assertEqual(updated, overseas_assets.static_block(updated, '/var/www/llm-static/gateway-archive'))
        for config in [original + original, original.replace('/static/llm-brand/', '/other/')]:
            with self.assertRaises(ValueError):
                overseas_assets.static_block(config, '/var/www/archive')
        with self.assertRaises(ValueError):
            overseas_assets.static_block(original, '/var/www/../private')

    def test_overseas_publication_rolls_back_and_retains_immutable_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / 'dist'
            (dist / 'static/js').mkdir(parents=True)
            html = '<script src="/static/js/index.new.js"></script>'
            (dist / 'index.html').write_text(html)
            (dist / 'static/js/index.new.js').write_text('new()')
            bundle = root / 'assets.tar.gz'
            assets.pack(dist, bundle, 'new')
            config = root / 'site.conf'
            original = b'server { location / { proxy_pass http://old; } }'
            config.write_bytes(original)
            archive = root / 'archive'
            (archive / 'static/js').mkdir(parents=True)
            (archive / 'static/js/index.old.js').write_text('old()')
            args = SimpleNamespace(root=archive, bundle=[(bundle, 'candidate')], config=config,
                                   backup=root / 'backups', public_url='https://example.test')
            locks = SimpleNamespace(LOCK_EX=1, LOCK_NB=2, flock=lambda *args: None)

            def run(*command, input=None):
                if 'wget' in command:
                    return html.encode()
                if command[:2] == ('docker', 'inspect'):
                    if '.NetworkSettings.Networks' in command[3]:
                        return b'{"network":{"IPAddress":"127.0.0.1"}}'
                    return b'sha256:image'
                return b''

            with patch.dict('sys.modules', {'fcntl': locks}), patch.object(overseas_assets, 'static_block', return_value='candidate'), patch.object(overseas_assets.assets, 'run', side_effect=run) as commands, patch.object(overseas_assets.assets, 'get_asset'):
                with patch.object(overseas_assets.assets, 'verify', side_effect=ValueError('public checksum failed')):
                    with self.assertRaisesRegex(ValueError, 'checksum'):
                        overseas_assets.publish(args)
                self.assertEqual(config.read_bytes(), original)
                self.assertEqual(sum(call.args == ('systemctl', 'reload', 'nginx') for call in commands.call_args_list), 2)
                with patch.object(overseas_assets.assets, 'verify'):
                    overseas_assets.publish(args)
                self.assertEqual(config.read_text(), 'candidate')
                self.assertEqual((archive / 'static/js/index.old.js').read_text(), 'old()')
                (dist / 'static/js/index.new.js').write_text('different()')
                assets.pack(dist, bundle, 'collision')
                with self.assertRaisesRegex(ValueError, 'overwrite'):
                    overseas_assets.publish(args)
                self.assertEqual((archive / 'static/js/index.new.js').read_text(), 'new()')
                self.assertEqual(config.read_text(), 'candidate')

    def test_new_hash_and_async_chunks_are_packed_with_old_build_independent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'static/js/async').mkdir(parents=True)
            (root / 'index.html').write_text('<script src="/static/js/index.new.js"></script>')
            (root / 'static/js/index.new.js').write_text('import("./async/page.new.js")')
            (root / 'static/js/async/page.new.js').write_text('newPage()')
            bundle = root / 'build.tar.gz'
            manifest = assets.pack(root, bundle, 'new')
            self.assertEqual(manifest['entries'], ['/static/js/index.new.js'])
            self.assertIn('/static/js/async/page.new.js', manifest['files'])
            with tarfile.open(bundle) as archive:
                self.assertEqual(set(archive.getnames()), {'manifest.json', 'static/js/index.new.js', 'static/js/async/page.new.js'})
            (root / 'static/js/index.new.js').unlink()
            with self.assertRaisesRegex(ValueError, 'incomplete'):
                assets.pack(root, bundle, 'broken')

    def test_static_route_is_build_independent_and_preserves_other_services(self):
        old = '''prefix /test unchanged
    # BEGIN PRICING RELEASE STATIC ASSETS
    @pricing_new_assets path /static/js/index.old.js
    handle @pricing_new_assets {
        reverse_proxy new-api-green:3000
    }
    @pricing_legacy_assets path /static/js/legacy.js
    handle @pricing_legacy_assets {
        rewrite * /release-static-old{uri}
        reverse_proxy api-docs:80
    }
    handle /static/* {
        reverse_proxy old:3000
    }
    # END PRICING RELEASE STATIC ASSETS
suffix backend unchanged
'''
        updated = assets.static_block(old, 'api-docs:80')
        self.assertNotIn('index.old.js', updated)
        self.assertIn('/release-static-old{uri}', updated)
        self.assertIn('/gateway-assets{uri}', updated)
        self.assertTrue(updated.startswith('prefix /test unchanged\n'))
        self.assertTrue(updated.endswith('suffix backend unchanged\n'))
        self.assertEqual(updated, assets.static_block(updated, 'api-docs:80'))
        with self.assertRaisesRegex(ValueError, 'exactly one'):
            assets.static_block(old + old, 'api-docs:80')

    def test_200_html_wrong_mime_corruption_and_missing_file_stop_release(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                body, mime, status = {
                    '/new.js': (b'new()', 'text/javascript', 200),
                    '/old.js': (b'old()', 'application/javascript', 200),
                    '/html.js': (b'<!DOCTYPE html><html>homepage', 'text/html', 200),
                    '/disguised.js': (b'<html>homepage', 'text/javascript', 200),
                    '/mime.js': (b'new()', 'text/plain', 200),
                    '/missing.js': (b'missing', 'text/plain', 404),
                }.get(self.path, (b'', 'text/plain', 404))
                self.send_response(status)
                self.send_header('Content-Type', mime)
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = HTTPServer(('127.0.0.1', 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        base = f'http://127.0.0.1:{server.server_port}'
        try:
            assets.verify({'release': 'mixed-slots', 'files': {'/new.js': assets.digest(b'new()'), '/old.js': assets.digest(b'old()')}}, base)
            for path, message in [('/html.js', 'HTML'), ('/disguised.js', 'HTML'), ('/mime.js', 'Content-Type'), ('/new.js', 'checksum'), ('/missing.js', '404')]:
                with self.subTest(path=path), self.assertRaisesRegex(ValueError, message):
                    assets.get_asset(base + path, 'bad-checksum')
        finally:
            server.shutdown()
            worker.join()
            server.server_close()

    def test_path_traversal_is_rejected(self):
        for name in ['/static/../secret', '/static/x?y', '/etc/passwd']:
            with self.subTest(name=name), self.assertRaises(ValueError):
                assets.asset_path(name)

    def test_publication_keeps_old_assets_and_rolls_config_back_on_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / 'dist'
            (dist / 'static/js').mkdir(parents=True)
            html = '<script src="/static/js/index.new.js"></script>'
            (dist / 'index.html').write_text(html)
            (dist / 'static/js/index.new.js').write_text('new()')
            bundle = root / 'assets.tar.gz'
            assets.pack(dist, bundle, 'new')
            caddyfile = root / 'Caddyfile'
            original = b'outside\n# BEGIN PRICING RELEASE STATIC ASSETS\nold\n# END PRICING RELEASE STATIC ASSETS\noutside'
            caddyfile.write_bytes(original)
            archive = root / 'archive'
            (archive / 'static/js').mkdir(parents=True)
            (archive / 'static/js/index.old.js').write_text('old()')
            args = SimpleNamespace(root=archive, bundle=bundle, caddyfile=caddyfile, container='candidate', caddy_container='caddy', archive_upstream='api-docs:80', public_url='https://example.test')
            locks = SimpleNamespace(LOCK_EX=1, LOCK_NB=2, flock=lambda *args: None)

            def docker(*command, input=None):
                if command[1] == 'inspect':
                    if '.NetworkSettings.Networks' in command[3]:
                        return b'{"network":{"IPAddress":"127.0.0.1"}}'
                    return b'sha256:image'
                if 'wget' in command:
                    return html.encode()
                return b''

            with patch.dict('sys.modules', {'fcntl': locks}), patch.object(assets, 'run', side_effect=docker) as commands, patch.object(assets, 'get_asset'):
                with patch.object(assets, 'verify', side_effect=ValueError('public MIME check failed')):
                    with self.assertRaisesRegex(ValueError, 'MIME'):
                        assets.publish(args)
                self.assertEqual(caddyfile.read_bytes(), original)
                self.assertIn('/tmp/Caddyfile.static.rollback', str(commands.call_args_list))
                with patch.object(assets, 'verify'):
                    assets.publish(args)
                self.assertIn(assets.BEGIN, caddyfile.read_text())
                self.assertEqual((archive / 'static/js/index.old.js').read_text(), 'old()')
                record = json.loads((archive / 'releases/sha256-image.json').read_text())
                self.assertEqual(record['entries'], ['/static/js/index.new.js'])
                # Reusing the same path with different bytes is rejected before
                # replacing either configuration or an already published file.
                (dist / 'static/js/index.new.js').write_text('different()')
                assets.pack(dist, bundle, 'collision')
                before = caddyfile.read_bytes()
                with self.assertRaisesRegex(ValueError, 'overwrite'):
                    assets.publish(args)
                self.assertEqual(caddyfile.read_bytes(), before)
                self.assertEqual((archive / 'static/js/index.new.js').read_text(), 'new()')


if __name__ == '__main__':
    unittest.main()
