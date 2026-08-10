from __future__ import annotations

import importlib.util
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "strict_dotenv.py"
SPEC = importlib.util.spec_from_file_location("strict_dotenv", SCRIPT)
assert SPEC and SPEC.loader
dotenv = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = dotenv
SPEC.loader.exec_module(dotenv)


class StrictDotenvTests(unittest.TestCase):
    def test_accepts_literal_allowlisted_values(self) -> None:
        values = dotenv.parse_dotenv_text(
            "# deployment\nCLAW_ENVIRONMENT=prod\nWORKBENCH_FILES_ENABLED=false\n"
        )
        self.assertEqual(
            {"CLAW_ENVIRONMENT": "prod", "WORKBENCH_FILES_ENABLED": "false"},
            values,
        )

    def test_rejects_shell_expansion_and_command_substitution(self) -> None:
        for value in ("${HOME}", "$(id)", "`id`"):
            with self.subTest(value=value), self.assertRaises(dotenv.DotenvError):
                dotenv.parse_dotenv_text(f"CLAW_ENVIRONMENT={value}\n")

    def test_rejects_unknown_duplicate_and_quoted_values(self) -> None:
        cases = (
            "LD_PRELOAD=/tmp/evil.so\n",
            "CLAW_ENVIRONMENT=prod\nCLAW_ENVIRONMENT=dev\n",
            'CLAW_ENVIRONMENT="prod"\n',
        )
        for text in cases:
            with self.subTest(text=text), self.assertRaises(dotenv.DotenvError):
                dotenv.parse_dotenv_text(text)

    def test_example_declares_every_allowed_key_once(self) -> None:
        example = SCRIPT.parents[1] / ".env.example"
        values = dotenv.parse_dotenv_text(example.read_text(encoding="utf-8"))
        self.assertEqual(dotenv.ALLOWED_KEYS, set(values))

    def test_deployment_urls_must_be_exact_and_internal(self) -> None:
        dotenv.validate_deployment_values({
            "WORKBENCH_CANONICAL_ORIGIN": "https://gateway.example.com",
            "NEW_API_INTERNAL_UPSTREAM": "http://new-api-green:3000",
            "NEW_API_INTERNAL_ALLOWED_HOSTS": "new-api-blue,new-api-green",
        })
        invalid = (
            {"WORKBENCH_CANONICAL_ORIGIN": "https://user@example.com/path?x=1"},
            {"WORKBENCH_CANONICAL_ORIGIN": "https://example.com:8443"},
            {"NEW_API_INTERNAL_UPSTREAM": "https://public.example.com:443", "NEW_API_INTERNAL_ALLOWED_HOSTS": "public.example.com"},
            {"NEW_API_INTERNAL_UPSTREAM": "http://127.0.0.1:3000", "NEW_API_INTERNAL_ALLOWED_HOSTS": "127.0.0.1"},
            {"NEW_API_INTERNAL_UPSTREAM": "http://localhost:3000", "NEW_API_INTERNAL_ALLOWED_HOSTS": "localhost"},
            {"NEW_API_INTERNAL_UPSTREAM": "http://evil:3000", "NEW_API_INTERNAL_ALLOWED_HOSTS": "new-api"},
            {"NEW_API_INTERNAL_UPSTREAM": "http://new-api:3000/path", "NEW_API_INTERNAL_ALLOWED_HOSTS": "new-api"},
        )
        for values in invalid:
            with self.subTest(values=values), self.assertRaises(dotenv.DotenvError):
                dotenv.validate_deployment_values(values)

    @unittest.skipIf(os.name == "nt", "POSIX ownership and mode semantics are required")
    def test_file_must_be_current_user_private_regular_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / ".env"
            path.write_text("CLAW_ENVIRONMENT=prod\n", encoding="utf-8")
            path.chmod(0o600)
            self.assertEqual("prod", dotenv.load_dotenv(path)["CLAW_ENVIRONMENT"])
            path.chmod(0o640)
            with self.assertRaises(dotenv.DotenvError):
                dotenv.load_dotenv(path)
            path.chmod(stat.S_IRUSR | stat.S_IWUSR)


if __name__ == "__main__":
    unittest.main()
