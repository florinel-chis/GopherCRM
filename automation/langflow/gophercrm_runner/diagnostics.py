"""Bounded failure summaries; application test logs are not readiness evidence."""

import json
import re


def go_output(text):
    """Unwrap go test -json output while retaining non-Go diagnostics."""
    lines = []
    for line in text.splitlines(keepends=True):
        try:
            event = json.loads(line)
        except ValueError:
            event = None
        if (isinstance(event, dict) and isinstance(event.get('Package'), str)
                and event.get('Action') in {'start', 'run', 'pause', 'cont', 'output', 'pass', 'fail', 'skip'}):
            if isinstance(event.get('Output'), str):
                lines.append(event['Output'])
        else:
            lines.append(line)
    return ''.join(lines)


def go_failure_blocks(text):
    # In verbose/JSON mode assertions precede the --- FAIL line. Recover the
    # failed test's output by identity, including interleaved parallel tests.
    outputs, failed = {}, []
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if (not isinstance(event, dict) or not isinstance(event.get('Package'), str)
                or not isinstance(event.get('Test'), str)):
            continue
        key = (event['Package'], event['Test'])
        if event.get('Action') == 'output' and isinstance(event.get('Output'), str):
            outputs.setdefault(key, []).append(event['Output'])
        elif event.get('Action') == 'fail':
            failed.append(key)
    if failed:
        return ''.join(''.join(outputs.get(key, [])) for key in failed)
    blocks, active = [], False
    for line in go_output(text).splitlines(keepends=True):
        if re.match(r'^\s*--- FAIL: Test\S+', line):
            active = True
        elif active and line.strip() and not line[0].isspace():
            active = False
        if active:
            blocks.append(line)
    return ''.join(blocks)


def failure_summary(stdout, stderr, limit=12000):
    """Keep early Go assertions even when later passing tests fill the log."""
    tail = stdout + stderr
    failures = go_failure_blocks(stdout)
    if not failures:
        return tail[-limit:]
    heading = 'Go test failure diagnostics:\n'
    excerpt = heading + failures[:limit * 2 // 3] + '\nFinal command diagnostics:\n'
    excerpt = excerpt[:limit]
    remaining = max(0, limit - len(excerpt))
    return excerpt + (tail[-remaining:] if remaining else '')


def classification_text(error):
    stdout, stderr = getattr(error, 'stdout', None), getattr(error, 'stderr', None)
    if stdout is None or stderr is None:
        return str(error)
    text = go_output(stdout) + stderr
    if not go_failure_blocks(stdout):
        return text
    lines = []
    for line in text.splitlines():
        try:
            entry = json.loads(line)
        except ValueError:
            entry = None
        # Go suites intentionally log errors from mocked service calls. An
        # assertion failure is established separately above. Keep the assertion
        # diagnostics and all unrecognised/startup output, including real SQL,
        # disk, browser and configuration errors; ignore only service log records.
        if (isinstance(entry, dict) and all(isinstance(entry.get(key), str)
                for key in ('level', 'msg', 'service', 'method'))):
            continue
        lines.append(line)
    return '\n'.join(lines)
