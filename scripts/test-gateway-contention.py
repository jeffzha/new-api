import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / 'scripts' / 'inspect-gateway-contention.py'
spec = importlib.util.spec_from_file_location('contention', SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CaptureRegression(unittest.TestCase):
    def test_postgres_empty_and_multiline_lock_chains(self):
        scratch = SCRIPT.parents[1] / '.testtmp'
        scratch.mkdir(exist_ok=True)
        fixtures = [[], [{'pid': 101, 'blockers': [102], 'tables': ['agency_hub_agencies']},
                         {'pid': 102, 'blockers': [], 'tables': ['agency_hub_agencies']}]]
        for locks in fixtures:
            with self.subTest(locks=locks), tempfile.TemporaryDirectory(dir=scratch) as directory:
                output = Path(directory) / 'capture.jsonl'

                def command(args, **kwargs):
                    if args[0:2] == ['docker', 'inspect']:
                        return json.dumps([{'NetworkSettings': {'Networks': {'isolated': {'IPAddress': '127.0.0.1'}}}}])
                    self.assertIn('psql', args)
                    self.assertIn('BEGIN READ ONLY', kwargs['input'])
                    return json.dumps(locks, indent=2) + '\n'

                argv = ['inspect', '--slot', 'isolated-gateway', '--network', 'isolated',
                        '--public-url', 'https://example.invalid', '--database-container', 'isolated-db',
                        '--database-user', 'test', '--database-name', 'test', '--samples', '1',
                        '--interval', '2', '--output', str(output)]
                stream = io.StringIO()
                with patch('sys.argv', argv), patch.object(module, 'command', side_effect=command), \
                        patch.object(module, 'probe', return_value={'ok': True}), contextlib.redirect_stdout(stream):
                    module.main()
                record = json.loads(output.read_text())
                self.assertEqual(record['lockChain'], locks)
                self.assertNotIn('databaseError', record)
                self.assertEqual(json.loads(stream.getvalue()), record)


if __name__ == '__main__':
    unittest.main()
