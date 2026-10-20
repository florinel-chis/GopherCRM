"""Deterministic browser-test selection shared by CI and the installed runner.

Core build/lint/unit/race checks and frozen acceptance checks are never removed.
Paths select only trusted, literal spec names. Unknown/shared code goes full.
"""

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess


POLICY_VERSION = 1
SMOKE = "e2e/tests/health-smoke.spec.ts"
FEATURES = {
    "aeo": ("aeo-providers",),
    "company": ("admin-companies", "admin-customers", "admin-deals", "admin-leads", "leads-convert-api"),
    "customer": ("admin-customers", "customer-website", "admin-tickets", "leads-convert-api",
                 "admin-tasks", "admin-deals", "admin-companies", "admin-entity-suite"),
    "deal": ("admin-deals", "admin-deals-board", "admin-companies", "admin-customers"),
    "form": ("forms-public-api", "admin-leads", "leads-convert-api"),
    "label": ("labels", "admin-tasks", "admin-entity-suite"),
    "lead": ("admin-leads", "leads-sorting-search", "leads-notes-api", "leads-convert-api",
             "admin-deals", "forms-public-api", "admin-tasks", "admin-entity-suite",
             "customer-website", "admin-companies"),
    "task": ("admin-tasks", "labels", "admin-entity-suite"),
    "ticket": ("admin-tickets", "admin-entity-suite"),
    "user": ("admin-users", "settings", "login", "registration", "admin-entity-suite"),
}
PAGE_FEATURES = {
    "aeo": "aeo", "companies": "company", "customers": "customer", "deals": "deal",
    "forms": "form", "labels": "label", "leads": "lead", "tasks": "task",
    "tickets": "ticket", "users": "user",
}
SHARED_PREFIXES = (
    ".github/", "scripts/", "automation/", "migrations/", "cmd/",
    "internal/database/", "internal/models/", "internal/config/", "internal/middleware/", "internal/utils/",
    "internal/errors/", "internal/mailer/", "internal/forms/",
    "internal/handler/user_", "internal/service/user_", "internal/repository/user_",
    "gocrm-ui/src/api/client", "gocrm-ui/src/contexts/", "gocrm-ui/src/context/",
    "gocrm-ui/src/hooks/", "gocrm-ui/src/components/", "gocrm-ui/src/routes/",
    "gocrm-ui/src/layouts/", "gocrm-ui/src/types/", "gocrm-ui/e2e/helpers/",
    "gocrm-ui/e2e/fixtures/", "gocrm-ui/e2e/pages/",
)
SHARED_FILES = {
    "Makefile", "go.mod", "go.sum", ".gitignore", ".gitattributes",
    "internal/handler/routes.go", "internal/service/interfaces.go",
    "internal/repository/interfaces.go",
}
DOC_FILES = {"README.md", "CHANGELOG.md", "LICENSE", "CONTRIBUTING.md", "AGENTS.md"}


def feature_for(path):
    for layer in ("handler", "service", "repository", "models", "mocks"):
        prefix = f"internal/{layer}/"
        if path.startswith(prefix):
            name = PurePosixPath(path).name.lower()
            for feature in FEATURES:
                if name.startswith(feature + "_") or name in {feature + ".go", feature + "service.go"}:
                    return feature
    if path.startswith("internal/aeo/"):
        return "aeo"
    for group, feature in PAGE_FEATURES.items():
        if path.startswith(f"gocrm-ui/src/pages/{group}/"):
            return feature
        if path == f"gocrm-ui/src/api/endpoints/{group}.ts":
            return feature
    return None


def plan(paths, *, full=False):
    paths = sorted(set(paths))
    reasons, features, specs = [], set(), {SMOKE}
    if full:
        reasons.append("explicit full validation")
    if not paths:
        reasons.append("no reliable change set")
    for name in paths:
        path = PurePosixPath(name)
        if (not name or "\\" in name or path.is_absolute() or ".." in path.parts
                or any(ord(c) < 32 for c in name)):
            raise ValueError("Invalid changed path")
        if name in DOC_FILES or name.startswith("docs/") and name.endswith(".md"):
            continue
        if name in SHARED_FILES or name.startswith(SHARED_PREFIXES):
            reasons.append(f"shared or validation code: {name}")
            continue
        # Integration tests still run in the mandatory Go race suite. The
        # corresponding production paths determine browser coverage.
        if name.startswith(("internal/integration/", "test/", "tests/")) and name.endswith("_test.go"):
            continue
        if name.startswith("api/") and name.endswith((".json", ".yaml")):
            # Generated API contracts can describe unrelated endpoints.
            reasons.append(f"API contract: {name}")
            continue
        if name.startswith("gocrm-ui/e2e/tests/"):
            spec = name.removeprefix("gocrm-ui/")
            if re.fullmatch(r"e2e/tests/[a-z0-9-]+\.spec\.ts", spec):
                specs.add(spec)
            else:
                reasons.append(f"unmapped browser input: {name}")
            continue
        feature = feature_for(name)
        if feature:
            features.add(feature)
            specs.update(f"e2e/tests/{spec}.spec.ts" for spec in FEATURES[feature])
        else:
            reasons.append(f"unmapped path: {name}")
    return {
        "policy_version": POLICY_VERSION,
        "profile": "full" if reasons else "contextual",
        "reasons": sorted(set(reasons)) or ["smoke plus affected feature suites"],
        "changed_paths": paths,
        "features": sorted(features),
        "specs": [] if reasons else sorted(specs),
    }


def browser_command(command, selected):
    """Only narrow the canonical browser commands; keep every other gate."""
    command = tuple(command)
    if command[:2] != ("make", "e2e") or selected["profile"] == "full":
        return command
    if any(arg.startswith("SPECS=") for arg in command[2:]):
        raise ValueError("Browser command already has a selector")
    return command + ("SPECS=" + " ".join(selected["specs"]),)


def changed_paths(base, head, *, cwd=None):
    # Both sides of renames and deletions must participate. No shell parsing,
    # glob expansion, abbreviated revisions or option-shaped refs.
    if not all(re.fullmatch(r"[0-9a-f]{40}", value) for value in (base, head)):
        raise ValueError("Validation requires full base and head commit SHAs")
    subprocess.run(["git", "merge-base", "--is-ancestor", base, head], cwd=cwd, check=True)
    result = subprocess.check_output(
        ["git", "diff", "--no-renames", "--name-only", "-z", base, head, "--"], cwd=cwd,
    )
    return result.decode("utf-8").rstrip("\0").split("\0") if result else []


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--full", action="store_true")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()
    selected = plan(changed_paths(args.base, args.head), full=args.full)
    # Deleted/renamed specs cannot make a contextual run silently shrink.
    if any(not (Path("gocrm-ui") / spec).is_file() for spec in selected["specs"]):
        selected = plan(selected["changed_paths"], full=True)
        selected["reasons"].append("selected spec is missing")
    selected.update(base=args.base, head=args.head)
    payload = json.dumps(selected, indent=2) + "\n"
    if args.output:
        args.output.write_text(payload)
    if args.github_output:
        with args.github_output.open("a") as stream:
            stream.write(f"profile={selected['profile']}\n")
            stream.write(f"specs={' '.join(selected['specs'])}\n")
    print(payload, end="")


if __name__ == "__main__":
    main()
