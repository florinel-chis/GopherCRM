#!/usr/bin/env python3
"""Fail when a tracked Go file is not gofmt-formatted.

Runs `gofmt -l` on every Go file git tracks and prints the ones it would
rewrite, one repository-relative path per line. Both paths of the Backend job
start it before `go build`: check-changed-go.py (pull requests) and
`make verify-backend` (full runs). The gofmt is the one on PATH (in CI the Go
version of go.mod), else the one next to `go`. To fix a failure:

    gofmt -w <the listed files>
"""

import argparse
from pathlib import Path
import sys

from go_checks import ROOT, GateError, check_format


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--root', type=Path, default=ROOT, help='the git work tree to check (default: this repository)')
    args = parser.parse_args()
    try:
        check_format(args.root.resolve())
    except GateError as error:
        print(error, file=sys.stderr, flush=True)
        raise SystemExit(1)


if __name__ == '__main__':
    main()
