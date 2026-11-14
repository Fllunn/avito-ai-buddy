#!/usr/bin/env python3
"""Собрать исходники и проверенные артефакты без локальных секретов/кэшей."""
from pathlib import Path
from zipfile import ZipFile, ZIP_DEFLATED
import hashlib
import json

root = Path(__file__).resolve().parent.parent
excluded = {'node_modules', 'dist', 'bin', 'coverage', '__pycache__', '.vite', '.venv', '.git'}
roots = ['backend', 'frontend', 'mocks', 'openspec', 'docs', 'infra', 'scripts', '.agents']
files = []
for directory in roots:
    for path in (root / directory).rglob('*'):
        rel = path.relative_to(root)
        if not path.is_file() or path.is_symlink() or any(part in excluded for part in rel.parts):
            continue
        if path.name.startswith('.env') or path.suffix in {'.log', '.out', '.pyc', '.tsbuildinfo'} or path.name == '.DS_Store':
            continue
        if path.name == 'acceptance-failure.png':
            continue
        files.append(path)
for name in ['AGENTS.md', 'CLAUDE.md', 'Dockerfile.go', 'Makefile', 'README.md', 'compose.yaml', '.env.example', '.dockerignore', '.editorconfig', '.gitattributes', '.gitignore']:
    files.append(root / name)
for name in ['coverage/summary.json', 'coverage/backend-native.txt', 'coverage/mocks-native.txt', 'output/pdf/ai-buddy-presentation.pdf', 'output/demo/ai-buddy-demo.mp4']:
    files.append(root / name)
assert all(path.is_file() for path in files)
relative = sorted({str(path.relative_to(root)) for path in files})
assert '.env' not in relative
out = root / 'output/submission/ai-buddy-submission.zip'
out.parent.mkdir(parents=True, exist_ok=True)
with ZipFile(out, 'w', ZIP_DEFLATED) as archive:
    for name in relative:
        archive.write(root / name, 'ai-buddy/' + name)
with ZipFile(out) as archive:
    assert archive.testzip() is None
    names = archive.namelist()
    assert all('/node_modules/' not in n and '/.git/' not in n and not n.endswith('/.env') for n in names)
    for required in ['README.md', 'compose.yaml', 'coverage/summary.json', 'output/pdf/ai-buddy-presentation.pdf', 'output/demo/ai-buddy-demo.mp4']:
        assert 'ai-buddy/' + required in names
report = {'file': str(out.relative_to(root)), 'files': len(relative), 'bytes': out.stat().st_size, 'sha256': hashlib.sha256(out.read_bytes()).hexdigest(), 'zip_integrity': 'pass', 'excluded': ['local env files', 'git metadata', 'dependencies', 'build caches', 'binaries', 'raw intermediate recordings'], 'mode': 'fake', 'human_acceptance': 'pending'}
(root / 'output/submission/manifest.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(report, ensure_ascii=False, indent=2))
