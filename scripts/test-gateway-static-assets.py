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


class StaticAssetsTests(unittest.TestCase):
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
