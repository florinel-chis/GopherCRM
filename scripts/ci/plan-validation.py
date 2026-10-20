#!/usr/bin/env python3
"""Run the selector from the trusted base, never the PR's proposed selector."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


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
    source = git("show", f"{base}:automation/langflow/gophercrm_runner/validation.py",
                 check=False) if available else None
    if source is None or source.returncode != 0:
        selected = {
            "profile": "full", "specs": [], "base": base, "head": head,
            "reasons": ["no trusted selector/change base; full validation required"],
        }
        plan_file.write_text(json.dumps(selected, indent=2) + "\n")
        with output.open("a") as stream:
            stream.write("profile=full\nspecs=\n")
    else:
        with tempfile.TemporaryDirectory() as folder:
            trusted = Path(folder) / "validation.py"
            trusted.write_text(source.stdout)
            args = ["python3", str(trusted), "--base", base, "--head", head,
                    "--output", str(plan_file), "--github-output", str(output)]
            if full:
                args.append("--full")
            subprocess.run(args, check=True)
    selected = json.loads(plan_file.read_text())
    print(json.dumps(selected, indent=2))
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with Path(summary).open("a") as stream:
            stream.write("### Validation selection\n\n```json\n")
            stream.write(json.dumps(selected, indent=2))
            stream.write("\n```\n")


if __name__ == "__main__":
    main()
