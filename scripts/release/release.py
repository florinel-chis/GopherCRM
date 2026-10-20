#!/usr/bin/env python3
"""Prepare versions and verify/publish the artifacts produced by full release CI."""

import argparse
from datetime import date
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

REPO = 'florinel-chis/GopherCRM'
BUNDLE = 'gophercrm-linux-amd64.tgz'
ASSETS = (BUNDLE, 'release-manifest.json', 'SHA256SUMS')
VERSION = r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def version(value):
    if not re.fullmatch(VERSION, value):
        raise ValueError('Use a stable SemVer version without v, for example 1.3.0')
    return tuple(map(int, value.split('.')))


def notes(text, heading):
    match = re.search(r'^## \[' + re.escape(heading) + r'\][^\n]*\n(.*?)(?=^## \[|^\[Unreleased\]:|\Z)',
                      text, re.M | re.S)
    if not match or not re.search(r'^- \S', match[1], re.M):
        raise ValueError(f'Missing substantive changelog entry: {heading}')
    return match[1].strip() + '\n'


def api_versions(root, expected, replacement=None):
    result = {}
    for name, pattern in (
        ('cmd/main.go', r'(?m)^(// @version )(\S+)$'),
        ('api/swagger.json', r'(?m)^(        "version": ")([^"\n]+)(")$'),
        ('api/swagger.yaml', r'(?m)^(  version: )(\S+)$'),
    ):
        path = root / name
        content = path.read_text()
        matches = list(re.finditer(pattern, content))
        if len(matches) != 1 or matches[0][2] != expected:
            raise ValueError(f'API version disagrees with package version: {name}')
        if replacement:
            match = matches[0]
            result[path] = content[:match.start(2)] + replacement + content[match.end(2):]
    return result


def prepare(root, value):
    version(value)
    if run('git', '-C', str(root), 'status', '--porcelain'):
        raise ValueError('Prepare a release in a clean worktree')
    changelog = root / 'CHANGELOG.md'
    text = changelog.read_text()
    current = json.loads((root / 'gocrm-ui/package.json').read_text())['version']
    if version(value) <= version(current) or f'## [{value}]' in text:
        raise ValueError('Version must increase and have no existing changelog entry')
    notes(text, 'Unreleased')
    text = text.replace('## [Unreleased]', f'## [Unreleased]\n\n## [{value}] - {date.today().isoformat()}', 1)
    old = f'[Unreleased]: https://github.com/{REPO}/compare/v{current}...HEAD'
    if old not in text:
        raise ValueError('Changelog comparison link does not match current version')
    text = text.replace(old, f'[Unreleased]: https://github.com/{REPO}/compare/v{value}...HEAD\n'
                            f'[{value}]: https://github.com/{REPO}/compare/v{current}...v{value}', 1)
    # Validate both JSON documents before writing any file.
    files = api_versions(root, current, value)
    for name in ('package.json', 'package-lock.json'):
        path = root / 'gocrm-ui' / name
        data = json.loads(path.read_text())
        if data['version'] != current:
            raise ValueError('Frontend versions disagree')
        data['version'] = value
        if name == 'package-lock.json':
            if data['packages']['']['version'] != current:
                raise ValueError('Lockfile root version disagrees')
            data['packages']['']['version'] = value
        files[path] = json.dumps(data, indent=2) + '\n'
    for path, content in files.items():
        path.write_text(content)
    changelog.write_text(text)


def metadata(root, value, revision, run_id, publish):
    if not re.fullmatch('[0-9a-f]{40}', revision) or run('git', '-C', str(root), 'rev-parse', 'HEAD') != revision:
        raise ValueError('Release checkout must equal the tested full commit SHA')
    if not str(run_id).isdigit():
        raise ValueError('Missing Actions run identity')
    if publish or value:
        version(value)
        for name in ('package.json', 'package-lock.json'):
            data = json.loads((root / 'gocrm-ui' / name).read_text())
            if data['version'] != value or (name == 'package-lock.json' and data['packages']['']['version'] != value):
                raise ValueError('Prepare and merge package versions before releasing')
        api_versions(root, value)
        entry = notes((root / 'CHANGELOG.md').read_text(), value)
    else:
        entry = notes((root / 'CHANGELOG.md').read_text(), 'Unreleased')
    return {'schema': 1, 'repository': REPO, 'revision': revision, 'run_id': str(run_id),
            'version': value or f'snapshot-{revision[:12]}', 'validation_profile': 'full',
            'publish': publish, 'artifact': BUNDLE}, entry


def digest(path):
    with path.open('rb') as stream:
        result = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(chunk)
        return result.hexdigest()


def seal(folder):
    # The manifest is included inside the archive as well as published separately.
    (folder / 'SHA256SUMS').write_text(''.join(
        f'{digest(folder / name)}  {name}\n' for name in ASSETS[:2]))


def verify(folder, revision=None, run_id=None, tag=None):
    lines = (folder / 'SHA256SUMS').read_text().splitlines()
    expected = [f'{digest(folder / name)}  {name}' for name in ASSETS[:2]]
    if lines != expected:
        raise ValueError('Release checksum mismatch')
    data = json.loads((folder / 'release-manifest.json').read_text())
    if (data.get('schema') != 1 or data.get('repository') != REPO
            or data.get('validation_profile') != 'full' or data.get('artifact') != BUNDLE
            or not re.fullmatch('[0-9a-f]{40}', data.get('revision', ''))
            or not re.fullmatch('[0-9]+', data.get('run_id', ''))):
        raise ValueError('Invalid release manifest')
    if ((revision and data['revision'] != revision) or (run_id and data['run_id'] != str(run_id))
            or (tag and (not data.get('publish') or tag != 'v' + data['version']))):
        raise ValueError('Release identity mismatch')
    with tarfile.open(folder / BUNDLE) as archive:
        members = {}
        for member in archive.getmembers():
            name = member.name.removeprefix('./').rstrip('/')
            if name in ('', '.') and member.isdir():
                continue
            if (name.startswith('/') or '..' in name.split('/') or name in members
                    or not (member.isfile() or member.isdir())):
                raise ValueError('Unsafe or duplicate archive member')
            members[name] = member
        required = ['bin/gophercrm', 'bin/create-admin', 'bin/migrate', 'www/index.html',
                    'REVISION', 'release-manifest.json', 'CHANGELOG.md', 'LICENSE']
        if any(name not in members or not members[name].isfile() for name in required):
            raise ValueError('Incomplete release archive')
        for name, expected_content in [('REVISION', data['revision']),
                                        ('release-manifest.json', (folder / 'release-manifest.json').read_text().strip())]:
            if members[name].size > 65536 or archive.extractfile(members[name]).read().decode().strip() != expected_content:
                raise ValueError('Archive metadata differs from release metadata')
    return data


def gh_json(*args):
    return json.loads(run('gh', *args))


def ensure_new_tag(tag):
    candidate = version(tag.removeprefix('v'))
    # A transport/authentication failure is an error, never evidence of absence.
    refs = run('git', 'ls-remote', '--tags', 'origin').splitlines()
    for line in refs:
        ref = line.split()[1].removeprefix('refs/tags/').removesuffix('^{}')
        if ref == tag:
            raise ValueError('Release tag already exists; inspect it, never move it')
        if re.fullmatch('v' + VERSION, ref) and version(ref[1:]) >= candidate:
            raise ValueError('Release must be newer than all stable tags')
    releases = gh_json('api', '--paginate', '--slurp', f'repos/{REPO}/releases?per_page=100')
    if any(release['tag_name'] == tag for page in releases for release in page):
        raise ValueError('A release or draft already exists; inspect it before retrying')


def publish(folder, note_file):
    data = verify(folder)
    tag = 'v' + data['version']
    version(data['version'])
    if not data['publish'] or os.environ.get('GITHUB_REF') != 'refs/heads/main':
        raise ValueError('Publishing requires explicit publish=true from main')
    if data['revision'] != os.environ.get('GITHUB_SHA') or data['run_id'] != os.environ.get('GITHUB_RUN_ID'):
        raise ValueError('Publication belongs to a different workflow execution')
    ensure_new_tag(tag)
    run('git', 'tag', '-a', tag, data['revision'], '-m', f'GopherCRM {tag}')
    run('git', 'push', 'origin', f'refs/tags/{tag}')
    run('gh', 'release', 'create', tag, '--repo', REPO, '--verify-tag', '--draft',
        '--title', f'GopherCRM {tag}', '--target', data['revision'],
        '--notes-file', str(note_file), '--generate-notes',
        *(str(folder / name) for name in ASSETS))
    with tempfile.TemporaryDirectory() as temp:
        run('gh', 'release', 'download', tag, '--repo', REPO, '--dir', temp)
        verify(Path(temp), data['revision'], data['run_id'], tag)
        for name in ASSETS:
            if digest(Path(temp) / name) != digest(folder / name):
                raise ValueError('Uploaded asset differs from the tested build')
    run('gh', 'release', 'edit', tag, '--repo', REPO, '--draft=false', '--latest')
    (folder / 'publication.json').write_text(json.dumps({'tag': tag, 'revision': data['revision'],
                                                       'run_id': data['run_id']}) + '\n')


def publication_count(run_id):
    if not run_id or not str(run_id).isdigit():
        raise ValueError('A numeric release run ID is required')
    # gh cannot combine --slurp and --jq. Parse the paginated result in Python.
    pages = gh_json('api', '--paginate', '--slurp',
                    f'repos/{REPO}/actions/runs/{run_id}/artifacts')
    return sum(artifact['name'] == 'release-publication' and not artifact['expired']
               for page in pages for artifact in page['artifacts'])


def download(folder, tag=None, triggering_run=None):
    if triggering_run:
        run('gh', 'run', 'download', str(triggering_run), '--repo', REPO,
            '--name', 'release-publication', '--dir', str(folder / 'receipt'))
        receipt = json.loads((folder / 'receipt/publication.json').read_text())
        if receipt['run_id'] != str(triggering_run):
            raise ValueError('Publication receipt belongs to another run')
        tag = receipt['tag']
    if not tag or not re.fullmatch('v' + VERSION, tag):
        raise ValueError('Deploy requires a published stable release tag')
    release = gh_json('api', f'repos/{REPO}/releases/tags/{tag}')
    latest = gh_json('api', f'repos/{REPO}/releases/latest')
    if release['draft'] or release['prerelease'] or latest['tag_name'] != tag:
        raise ValueError('Only the latest published stable release can deploy; use rollback for recovery')
    run('gh', 'release', 'download', tag, '--repo', REPO, '--dir', str(folder),
        *(arg for name in ASSETS for arg in ('--pattern', name)))
    data = verify(folder, run_id=triggering_run, tag=tag)
    refs = run('git', 'ls-remote', 'origin', f'refs/tags/{tag}', f'refs/tags/{tag}^{{}}').splitlines()
    peeled = {line.split()[1]: line.split()[0] for line in refs}
    if peeled.get(f'refs/tags/{tag}^{{}}', peeled.get(f'refs/tags/{tag}')) != data['revision']:
        raise ValueError('Tag no longer identifies the validated commit')
    execution = gh_json('api', f'repos/{REPO}/actions/runs/{data["run_id"]}')
    if (execution['path'] != '.github/workflows/release.yml' or execution['conclusion'] != 'success'
            or execution['event'] != 'workflow_dispatch' or execution['head_branch'] != 'main'
            or execution['head_sha'] != data['revision']):
        raise ValueError('Release lacks a successful full release workflow for this commit')
    if triggering_run and receipt['revision'] != data['revision']:
        raise ValueError('Publication receipt revision mismatch')
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'check', 'seal', 'verify', 'publish', 'download', 'publication-status'])
    parser.add_argument('--version', default='')
    parser.add_argument('--publish', action='store_true')
    parser.add_argument('--folder', type=Path, default=Path('release-artifacts'))
    parser.add_argument('--tag')
    parser.add_argument('--run-id')
    args = parser.parse_args()
    if args.action == 'prepare':
        prepare(Path.cwd(), args.version)
    elif args.action == 'check':
        if os.environ.get('GITHUB_REF') != 'refs/heads/main':
            raise ValueError('Release workflows must run from main')
        data, entry = metadata(Path.cwd(), args.version, os.environ['GITHUB_SHA'],
                               os.environ['GITHUB_RUN_ID'], args.publish)
        if args.publish:
            ensure_new_tag('v' + args.version)
        args.folder.mkdir(parents=True, exist_ok=True)
        (args.folder / 'release-manifest.json').write_text(json.dumps(data, indent=2) + '\n')
        (args.folder / 'release-notes.md').write_text(entry)
    elif args.action == 'seal':
        seal(args.folder)
        verify(args.folder)
    elif args.action == 'verify':
        print(json.dumps(verify(args.folder)))
    elif args.action == 'publication-status':
        print(publication_count(args.run_id))
    elif args.action == 'publish':
        publish(args.folder, args.folder / 'release-notes.md')
    else:
        args.folder.mkdir(parents=True, exist_ok=True)
        data = download(args.folder, args.tag, args.run_id)
        with open(os.environ['GITHUB_ENV'], 'a') as stream:
            stream.write('REVISION=' + data['revision'] + '\n')


if __name__ == '__main__':
    main()
