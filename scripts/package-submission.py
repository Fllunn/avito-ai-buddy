#!/usr/bin/env python3
"""Собрать архив сдачи из файлов git и явно перечисленных артефактов.

Состав строится по allowlist: только файлы, отслеживаемые git, в перечисленных
каталогах и корневые файлы, плюс явный список проверенных артефактов. Файл,
похожий на секрет, прерывает сборку до создания архива; содержимое не выводится.
"""
from pathlib import Path, PurePosixPath
from zipfile import ZipFile, ZIP_DEFLATED
import fnmatch
import hashlib
import json
import subprocess
import sys

ROOTS = ['backend', 'frontend', 'mocks', 'openspec', 'docs', 'infra', 'scripts', '.agents']
ROOT_FILES = ['AGENTS.md', 'CLAUDE.md', 'Dockerfile.go', 'Makefile', 'README.md', 'compose.yaml', '.env.example', '.dockerignore', '.editorconfig', '.gitattributes', '.gitignore']
ARTIFACTS = ['coverage/summary.json', 'coverage/backend-native.txt', 'coverage/mocks-native.txt', 'output/pdf/ai-buddy-presentation.pdf', 'output/demo/ai-buddy-demo.mp4']
REQUIRED = ['README.md', 'compose.yaml', 'coverage/summary.json', 'output/pdf/ai-buddy-presentation.pdf', 'output/demo/ai-buddy-demo.mp4']
SECRET_NAMES = ['*.pem', '*.key', '*.p12', '*.pfx', 'id_rsa*', 'id_ed25519*', '*secret*', '*credential*']
SKIPPED_SUFFIXES = {'.log', '.out', '.pyc', '.tsbuildinfo'}


class SecretFileError(Exception):
    def __init__(self, paths):
        super().__init__(', '.join(paths))
        self.paths = paths


def tracked_files(root: Path) -> list[str]:
    output = subprocess.run(['git', '-C', str(root), 'ls-files', '-z', '--', *ROOTS, *ROOT_FILES], capture_output=True, check=True).stdout
    return [name for name in output.decode('utf-8').split('\0') if name]


def looks_secret(name: str) -> bool:
    base = PurePosixPath(name).name.lower()
    if base.startswith('.env') and base != '.env.example':
        return True
    if base.endswith('_test.go') or base.endswith('.test.ts') or base.endswith('.test.tsx'):
        return False
    return any(fnmatch.fnmatch(base, pattern) for pattern in SECRET_NAMES)


def select_files(root: Path, artifacts: list[str] = ARTIFACTS) -> list[str]:
    names = {name for name in tracked_files(root) if PurePosixPath(name).suffix not in SKIPPED_SUFFIXES and PurePosixPath(name).name != '.DS_Store'}
    names.update(artifacts)
    missing = sorted(name for name in names if not (root / name).is_file() or (root / name).is_symlink())
    if missing:
        raise FileNotFoundError(', '.join(missing))
    suspicious = sorted(name for name in names if looks_secret(name))
    if suspicious:
        raise SecretFileError(suspicious)
    return sorted(names)


def build(root: Path, names: list[str]) -> dict:
    out = root / 'output/submission/ai-buddy-submission.zip'
    out.parent.mkdir(parents=True, exist_ok=True)
    with ZipFile(out, 'w', ZIP_DEFLATED) as archive:
        for name in names:
            archive.write(root / name, 'ai-buddy/' + name)
    with ZipFile(out) as archive:
        assert archive.testzip() is None
        entries = archive.namelist()
        assert all('/node_modules/' not in n and '/.git/' not in n and not looks_secret(n) for n in entries)
        for required in REQUIRED:
            assert 'ai-buddy/' + required in entries
    return {'file': str(out.relative_to(root)), 'files': len(names), 'bytes': out.stat().st_size, 'sha256': hashlib.sha256(out.read_bytes()).hexdigest(), 'zip_integrity': 'pass', 'selection': 'git ls-files + explicit artifacts', 'excluded': ['untracked files', 'local env files', 'secret-like names', 'git metadata', 'dependencies', 'build caches'], 'mode': 'fake', 'human_acceptance': 'pending'}


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    try:
        names = select_files(root)
    except SecretFileError as error:
        print('Сборка остановлена: файлы похожи на секреты: ' + ', '.join(error.paths), file=sys.stderr)
        return 1
    except FileNotFoundError as error:
        print('Сборка остановлена: нет файлов: ' + str(error), file=sys.stderr)
        return 1
    report = build(root, names)
    (root / 'output/submission/manifest.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == '__main__':
    sys.exit(main())
