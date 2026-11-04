#!/usr/bin/env python3
"""Use main's policy, or an operator-reviewed policy pinned to this exact PR head.

The complete browser suite runs only for a release, which asks for it with
FORCE_FULL. A change the policy cannot narrow gets the 'bounded' profile: every
backend and database gate of full, with only the browser specs it affects.
"""

import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

SMOKE = 'e2e/tests/health-smoke.spec.ts'


def present(specs):
    """Keep the specs that exist; a browser job with none would run the whole suite."""
    kept = sorted(spec for spec in specs if (Path('gocrm-ui') / spec).is_file())
    if SMOKE not in kept:
        raise SystemExit(f'Cannot bound this run: {SMOKE} is missing')
    return kept


def bounded(selected, selector):
    """Replace a full selection, keeping its reasons so the narrower run is visible."""
    spec = importlib.util.spec_from_file_location('trusted_validation', selector)
    policy = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(policy)
    wanted = set(selected.get('affected_specs') or
                 (f'e2e/tests/{name}.spec.ts' for feature in selected.get('features', [])
                  for name in policy.FEATURES[feature]))
    return {**selected, 'profile': 'bounded', 'specs': present(wanted | {SMOKE}),
            'reasons': [*selected['reasons'], 'bounded: the full suite runs only for a release']}


def git(*args, check=True):
    return subprocess.run(["git", *args], capture_output=True, text=True, check=check)


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    kind = os.environ["GITHUB_EVENT_NAME"]
    head = git("rev-parse", "HEAD").stdout.strip()
    base = {
        "pull_request": event.get("pull_request", {}).get("base", {}).get("sha"),
        "merge_group": event.get("merge_group", {}).get("base_sha"),
        "push": event.get("before"),
    }.get(kind)
    full = os.environ.get("FORCE_FULL") == "true"
    output = Path(os.environ["GITHUB_OUTPUT"])
    plan_file = Path(os.environ["RUNNER_TEMP"]) / "validation-plan.json"
    available = (isinstance(base, str) and re.fullmatch("[0-9a-f]{40}", base)
                 and base != "0" * 40
                 and git("merge-base", "--is-ancestor", base, head, check=False).returncode == 0)
    # A policy rollout must not require the obsolete policy's full suite merely
    # to correct it. This repository-admin variable authorizes ONE reviewed SHA;
    # PR text, branch names and candidate files cannot grant this exception.
    approved = os.environ.get('REVIEWED_VALIDATION_POLICY_SHA', '')
    policy_ref = base
    policy_origin = 'base'
    approved_candidate = available and approved == head
    if available and kind == 'pull_request' and approved:
        # Actions tests a synthetic merge commit, not the PR branch tip. Bind
        # the pin to that tip and require the exact base/tip parent pair.
        candidate = event.get('pull_request', {}).get('head', {}).get('sha')
        parents = git('rev-list', '--parents', '-n', '1', head).stdout.split()
        approved_candidate = approved == candidate and (
            head == candidate or parents == [head, base, candidate])
    if approved_candidate and re.fullmatch('[0-9a-f]{40}', approved):
        policy_ref, policy_origin = approved, 'operator-reviewed exact head'
    elif available and kind == 'push' and event.get('ref') == 'refs/heads/main':
        policy_ref, policy_origin = head, 'reviewed main'
    source = git("show", f"{policy_ref}:automation/langflow/gophercrm_runner/validation.py",
                 check=False) if available else None
    if source is None or source.returncode != 0:
        selected = {
            "profile": "full" if full else "bounded", "base": base, "head": head,
            "specs": [] if full else present({SMOKE}),
            "reasons": ["no trusted selector/change base" + ("" if full else "; smoke only")],
        }
    else:
        with tempfile.TemporaryDirectory() as folder:
            trusted = Path(folder) / "validation.py"
            trusted.write_text(source.stdout)
            args = ["python3", str(trusted), "--base", base, "--head", head,
                    "--output", str(plan_file)]
            if full:
                args.append("--full")
            subprocess.run(args, check=True)
            selected = json.loads(plan_file.read_text())
            if selected['profile'] == 'full' and not full:
                selected = bounded(selected, trusted)
    selected.update(policy_commit=policy_ref, policy_origin=policy_origin)
    plan_file.write_text(json.dumps(selected, indent=2) + '\n')
    with output.open('a') as stream:
        stream.write(f"profile={selected['profile']}\nspecs={' '.join(selected['specs'])}\n")
    print(json.dumps(selected, indent=2))
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with Path(summary).open("a") as stream:
            stream.write("### Validation selection\n\n```json\n")
            stream.write(json.dumps(selected, indent=2))
            stream.write("\n```\n")


if __name__ == "__main__":
    main()
