"""Тесты allowlist-сборки архива сдачи: python -m unittest scripts/test_package_submission.py."""
from pathlib import Path
import importlib.util
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('package_submission', Path(__file__).with_name('package-submission.py'))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class SelectionTest(unittest.TestCase):
    def repo(self, tracked: dict[str, str], untracked: dict[str, str]) -> Path:
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        subprocess.run(['git', 'init', '-q', str(root)], check=True)
        for files in (tracked, untracked):
            for name, text in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text, encoding='utf-8')
        if tracked:
            subprocess.run(['git', '-C', str(root), 'add', '--', *tracked], check=True)
        return root

    def test_untracked_files_are_not_selected(self):
        root = self.repo({'README.md': 'demo', 'backend/main.go': 'package main', 'backend/main_test.go': 'package main'}, {'infra/deploy.env': 'synthetic', 'backend/notes.txt': 'local'})
        names = package.select_files(root, artifacts=[])
        self.assertEqual(names, ['README.md', 'backend/main.go', 'backend/main_test.go'])

    def test_secret_like_files_stop_the_build(self):
        for name in ['infra/deploy.pem', 'backend/.env.local', 'docs/service-secret.json', 'scripts/id_rsa', 'infra/tls.key']:
            with self.subTest(name=name):
                root = self.repo({'README.md': 'demo', name: 'synthetic-placeholder-value'}, {})
                with self.assertRaises(package.SecretFileError) as raised:
                    package.select_files(root, artifacts=[])
                self.assertEqual(raised.exception.paths, [name])
                self.assertNotIn('synthetic-placeholder-value', str(raised.exception))

    def test_secret_like_artifact_stops_the_build(self):
        root = self.repo({'README.md': 'demo'}, {'output/credentials.txt': 'synthetic'})
        with self.assertRaises(package.SecretFileError):
            package.select_files(root, artifacts=['output/credentials.txt'])

    def test_env_example_and_test_files_are_allowed(self):
        for name in ['.env.example', 'backend/secret_test.go', 'frontend/src/secret.test.ts']:
            with self.subTest(name=name):
                self.assertFalse(package.looks_secret(name))

    def test_missing_artifact_is_reported(self):
        root = self.repo({'README.md': 'demo'}, {})
        with self.assertRaises(FileNotFoundError):
            package.select_files(root, artifacts=['coverage/summary.json'])

    def test_current_tree_has_no_secret_like_files(self):
        root = Path(__file__).resolve().parent.parent
        names = package.select_files(root, artifacts=[])
        self.assertIn('compose.yaml', names)
        self.assertIn('.env.example', names)
        self.assertNotIn('.env', names)


if __name__ == '__main__':
    unittest.main()
