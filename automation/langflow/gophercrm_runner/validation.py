"""Deterministic contextual validation shared by CI and the installed runner.

Runner-only changes use runner/hygiene tests and application health smoke.
Bounded backend profiles use targeted Go checks; unmapped shared app code goes full.
"""

import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile


POLICY_VERSION = 9
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
RUNNER_PREFIXES = ("automation/langflow/", "scripts/ci/")
RUNNER_FILES = {".github/workflows/ci.yml", ".github/workflows/remediation-runner.yml"}
MIGRATION_WORKFLOW_FILES = {
    '.github/workflows/migrations.yml', 'scripts/release/check-sql-tests.py',
    'scripts/release/tests/test_release.py', 'scripts/release/tests/test_sql_coverage.py',
}
AUTH_FILES = {
    'internal/service/auth_service.go', 'internal/handler/auth_handler.go',
    'internal/handler/auth_registration.go', 'scripts/ci/check-auth.py',
}
AUTH_TEST = re.compile(r'(?:internal/(?:service|handler)/auth[a-z0-9_]*_test|tests/auth[a-z0-9_]*_test)\.go$')
AUTH_PERSISTENCE_FILES = {
    'internal/repository/refresh_token_repository.go',
    'internal/repository/refresh_token_repository_test.go',
    'internal/mocks/RefreshTokenRepository.go',
}
REPOSITORY_INTERFACES = 'internal/repository/interfaces.go'
ROTATION_ROOTS = {f'TestRefreshRotation{boundary}{engine}'
                  for boundary in ('Atomicity', 'Concurrent', 'Validation')
                  for engine in ('SQLite', 'MySQL')}
GO_LEXEME = re.compile(
    r'(?P<ignore>\s+|//[^\n]*|/\*.*?\*/)|'
    r'(?P<literal>`[^`]*`|"(?:\\.|[^"\\\n])*"|\'(?:\\.|[^\'\\\n])*\')|'
    r'(?P<word>[^\W\d]\w*)|(?P<symbol>.)', re.DOTALL)
TRANSACTION_PATH = re.compile(r'internal/utils/transaction(?:\.go|(?:_[a-z0-9_]+)?_test\.go)$')
AUTH_SPECS = {SMOKE, 'e2e/tests/login.spec.ts', 'e2e/tests/registration.spec.ts'}
MAILER_FILES = {'internal/mailer/smtp.go'}
MAILER_TEST = re.compile(r'internal/mailer/[a-z0-9_]+_test\.go$')
MAILER_REQUIRED_ROOTS = {'TestSMTPDeadlines', 'TestSMTPHeaders', 'TestSMTPTLSAuth',
                         'TestSMTPFailureRedaction'}


AEO_MAIN_PROOF = r'''
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
)

func rendered(n interface{}) string {
	var b bytes.Buffer
	if format.Node(&b, token.NewFileSet(), n) != nil {
		panic("format")
	}
	return b.String()
}
func assignment(s ast.Stmt, allowed map[string]bool) bool {
	a, ok := s.(*ast.AssignStmt)
	if !ok || a.Tok != token.DEFINE {
		return false
	}
	for _, n := range a.Lhs {
		i, ok := n.(*ast.Ident)
		if !ok || !allowed[i.Name] {
			return false
		}
	}
	return len(a.Lhs) > 0
}
func callName(s ast.Stmt) string {
	e, ok := s.(*ast.ExprStmt)
	if !ok {
		return ""
	}
	c, ok := e.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	return rendered(c.Fun)
}
func dependencies(s ast.Stmt) bool {
	if b, ok := s.(*ast.BlockStmt); ok {
		return len(b.List) == 1 && dependencies(b.List[0])
	}
	if e, ok := s.(*ast.ExprStmt); ok {
		if c, ok := e.X.(*ast.CallExpr); ok {
			return rendered(c) == "setupDependencies(backgroundCtx, api, cfg)"
		}
	}
	if a, ok := s.(*ast.AssignStmt); ok && a.Tok == token.DEFINE && len(a.Lhs) == 1 && rendered(a.Lhs[0]) == "stopAEO" && len(a.Rhs) == 1 {
		c, ok := a.Rhs[0].(*ast.CallExpr)
		return ok && rendered(c) == "setupDependencies(backgroundCtx, api, cfg)"
	}
	return false
}
func validResults(fn *ast.FuncDecl) bool {
	fields := fn.Type.Results
	if fn.Name.Name == "setupDependencies" {
		return fields == nil || len(fields.List) == 1 && len(fields.List[0].Names) == 0 && rendered(fields.List[0].Type) == "func(context.Context) error"
	}
	if fn.Name.Name == "setupRouter" {
		if fields == nil || len(fields.List) < 1 || len(fields.List) > 2 || len(fields.List[0].Names) != 0 || rendered(fields.List[0].Type) != "*gin.Engine" {
			return false
		}
		return len(fields.List) == 1 || len(fields.List[1].Names) == 0 && rendered(fields.List[1].Type) == "func(context.Context) error"
	}
	return true
}
func skeleton(path string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if name != "main" && name != "setupRouter" && name != "setupDependencies" {
			continue
		}
		if seen[name] || fn.Recv != nil || fn.Body == nil || !validResults(fn) {
			return "", fmt.Errorf("unexpected declaration")
		}
		seen[name] = true
		kept := []ast.Stmt{}
		boundary := false
		router := false
		dependency := false
		returns := 0
		for index, s := range fn.Body.List {
			switch name {
			case "main":
				if e, ok := s.(*ast.ExprStmt); ok {
					if c, ok := e.X.(*ast.CallExpr); ok && rendered(c.Fun) == "utils.Logger.Info" && len(c.Args) == 1 && rendered(c.Args[0]) == `"Shutting down server..."` {
						boundary = true
						break
					}
				}
				if boundary {
					continue
				}
				if a, ok := s.(*ast.AssignStmt); ok && len(a.Rhs) == 1 {
					if c, ok := a.Rhs[0].(*ast.CallExpr); ok && rendered(c.Fun) == "setupRouter" {
						if router || rendered(c) != "setupRouter(backgroundCtx, cfg)" || len(a.Lhs) == 0 || len(a.Lhs) > 2 || rendered(a.Lhs[0]) != "router" || len(a.Lhs) == 2 && rendered(a.Lhs[1]) != "stopAEO" {
							return "", fmt.Errorf("router assignment")
						}
						router = true
						a.Lhs = []ast.Expr{ast.NewIdent("router")}
					}
				}
			case "setupRouter":
				if dependencies(s) {
					if dependency {
						return "", fmt.Errorf("duplicate dependency call")
					}
					dependency = true
					continue
				}
				if r, ok := s.(*ast.ReturnStmt); ok {
					if index != len(fn.Body.List)-1 || len(r.Results) < 1 || len(r.Results) > 2 || rendered(r.Results[0]) != "router" || len(r.Results) == 2 && rendered(r.Results[1]) != "stopAEO" {
						return "", fmt.Errorf("router return")
					}
					returns++
					continue
				}
			case "setupDependencies":
				if assignment(s, map[string]bool{"aeoProviders": true, "aeoEngine": true, "aeoService": true, "aeoRuntime": true}) {
					continue
				}
				if i, ok := s.(*ast.IfStmt); ok {
					if a, ok := i.Init.(*ast.AssignStmt); ok && len(a.Rhs) == 1 {
						if c, ok := a.Rhs[0].(*ast.CallExpr); ok && rendered(c.Fun) == "aeoService.ReconcileRunningRuns" {
							continue
						}
					}
					cond := rendered(i.Cond)
					if cond == "cfg.AEO.ScheduleEnabled" || cond == "cfg.AEO.ScheduleEnabled && len(aeoProviders) > 0" {
						continue
					}
				}
				if _, ok := s.(*ast.ReturnStmt); ok {
					if index != len(fn.Body.List)-1 {
						return "", fmt.Errorf("early dependency return")
					}
					continue
				}
			}
			if !boundary {
				kept = append(kept, s)
			}
		}
		if name == "main" && (!boundary || !router) {
			return "", fmt.Errorf("missing lifecycle boundary")
		}
		if name == "setupRouter" && (!dependency || returns != 1) {
			return "", fmt.Errorf("missing router wiring")
		}
		fn.Body.List = kept
		if name == "setupRouter" || name == "setupDependencies" {
			fn.Type.Results = nil
		}
	}
	if len(seen) != 3 {
		return "", fmt.Errorf("missing startup functions")
	}
	var scan scanner.Scanner
	src := []byte(rendered(f))
	scan.Init(token.NewFileSet().AddFile("", -1, len(src)), src, nil, 0)
	var out bytes.Buffer
	for {
		_, tok, lit := scan.Scan()
		if tok == token.EOF {
			break
		}
		fmt.Fprintf(&out, "%d:%q;", tok, lit)
	}
	return out.String(), nil
}
func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	a, e := skeleton(os.Args[1])
	b, f := skeleton(os.Args[2])
	if e != nil || f != nil || a != b {
		os.Exit(1)
	}
}
'''

AEO_FILES = {'internal/service/aeo_service.go', 'internal/service/aeo_credentials.go', 'internal/service/aeo_runtime.go',
             'internal/repository/aeo_repository.go', 'internal/handler/aeo_handler.go',
             'internal/mocks/AEORepository.go', 'internal/mocks/AEOService.go',
             'cmd/aeo_runtime.go'}
AEO_PATH = re.compile(r'(?:internal/aeo/[a-z0-9_]+|internal/(?:service|repository|handler)/aeo[a-z0-9_]*_test|cmd/aeo[a-z0-9_]*_test)\.go$')
AEO_SPECS = {SMOKE, 'e2e/tests/aeo-providers.spec.ts'}
AEO_REQUIRED_ROOTS = {
    'cmd': {'TestAEORuntimeStartup', 'TestAEORuntimeShutdown'},
    'internal/service': {f'TestAEO{boundary}{engine}' for boundary in ('RunLifecycle', 'ConcurrentShutdown')
                         for engine in ('SQLite', 'MySQL')},
}

def aeo_roots_present(head, *, cwd=None):
    if not isinstance(head, str) or not re.fullmatch(r'[0-9a-f]{40}', head):
        return False
    for package, required in AEO_REQUIRED_ROOTS.items():
        result = subprocess.run(['git', 'grep', '-h', '-E', '^func[[:space:]]+TestAEO', head, '--',
                                 f':(glob){package}/aeo*_test.go'], cwd=cwd, capture_output=True, text=True)
        names = re.findall(r'^func\s+(Test[A-Za-z0-9_]+)\s*\(\s*\w+\s+\*testing\.T\s*\)',
                           result.stdout, re.MULTILINE)
        if result.returncode or any(names.count(name) != 1 for name in required):
            return False
    return True

def aeo_main_only(base, head, *, cwd=None):
    if not all(isinstance(ref, str) and re.fullmatch(r'[0-9a-f]{40}', ref) for ref in (base, head)):
        return False
    with tempfile.TemporaryDirectory() as folder:
        root = Path(folder)
        (root / 'proof.go').write_text(AEO_MAIN_PROOF)
        for label, ref in [('base', base), ('head', head)]:
            result = subprocess.run(['git', 'show', f'{ref}:cmd/main.go'], cwd=cwd, capture_output=True, text=True)
            if result.returncode:
                return False
            (root / (label + '.txt')).write_text(result.stdout)
        try:
            result = subprocess.run(['go', 'run', str(root / 'proof.go'), str(root / 'base.txt'), str(root / 'head.txt')],
                                    cwd=root, capture_output=True, text=True, timeout=60)
            return result.returncode == 0
        except (OSError, subprocess.TimeoutExpired):
            return False


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


def without_refresh_interface_body(source, name='RefreshTokenRepository'):
    """Mask one top-level interface body using Go lexical delimiters.

    Comments and literals cannot supply braces or fake declarations. Matching
    balanced braces also prevents an inline `}; func ...` from hiding a new
    top-level declaration. Compilation remains the syntax/semantic authority.
    """
    tokens = []
    for match in GO_LEXEME.finditer(source):
        if match.lastgroup == 'ignore':
            continue
        token = match.group()
        if match.lastgroup == 'symbol' and (token in ('"', "'", '`') or
                                            source[match.start():match.start() + 2] == '/*'):
            return None
        tokens.append((token if match.lastgroup != 'literal' else '<literal>', match.start(), match.end()))
    depth, spans = 0, []
    for index, (token, _, _) in enumerate(tokens):
        if depth == 0 and [t[0] for t in tokens[index:index + 4]] == [
                'type', name, 'interface', '{']:
            nested = 1
            for later in tokens[index + 4:]:
                nested += (later[0] == '{') - (later[0] == '}')
                if nested == 0:
                    spans.append((tokens[index + 3][2], later[1]))
                    break
        depth += (token == '{') - (token == '}')
        if depth < 0:
            return None
    if depth or len(spans) != 1:
        return None
    start, end = spans[0]
    return source[:start] + '<refresh-interface-body>' + source[end:]


def refresh_interface_only(base, head, *, cwd=None):
    """Prove the shared file changed only inside the refresh-token interface.

    Path-only previews cannot grant this exception. Missing/deleted files,
    unexpected formatting or another changed declaration retain full coverage.
    """
    if not all(isinstance(ref, str) and re.fullmatch(r'[0-9a-f]{40}', ref) for ref in (base, head)):
        return False
    bodies, masks = [], []
    for ref in (base, head):
        result = subprocess.run(['git', 'show', f'{ref}:{REPOSITORY_INTERFACES}'],
                                cwd=cwd, capture_output=True, text=True)
        if result.returncode:
            return False
        bodies.append(result.stdout)
        masks.append(without_refresh_interface_body(result.stdout))
    return bodies[0] != bodies[1] and masks[0] is not None and masks[0] == masks[1]


def rotation_roots_present(head, *, cwd=None):
    """Require the six maintained boundary roots in the candidate, not main."""
    if not isinstance(head, str) or not re.fullmatch(r'[0-9a-f]{40}', head):
        return False
    pattern = r'^func[[:space:]]+TestRefreshRotation(Atomicity|Concurrent|Validation)(SQLite|MySQL)[[:space:]]*\('
    result = subprocess.run(['git', 'grep', '-h', '-E', pattern, head, '--',
                             ':(glob)internal/service/auth*_test.go'],
                            cwd=cwd, capture_output=True, text=True)
    names = re.findall(r'^func\s+(Test[A-Za-z0-9_]+)\s*\(\s*\w+\s+\*testing\.T\s*\)',
                       result.stdout, re.MULTILINE)
    return result.returncode == 0 and len(names) == len(ROTATION_ROOTS) and set(names) == ROTATION_ROOTS


def aeo_interface_only(base, head, *, cwd=None):
    if not all(isinstance(ref, str) and re.fullmatch(r'[0-9a-f]{40}', ref) for ref in (base, head)):
        return False
    masks = []
    for ref in (base, head):
        result = subprocess.run(['git', 'show', f'{ref}:{REPOSITORY_INTERFACES}'], cwd=cwd, capture_output=True, text=True)
        if result.returncode:
            return False
        masks.append(without_refresh_interface_body(result.stdout, 'AEORepository'))
    return masks[0] is not None and masks[0] == masks[1]


def env_comments_only(base, head, *, cwd=None):
    """Prove only comments/blank lines changed in the root configuration example.

    Keep this deliberately narrower than a dotenv parser: quoted, escaped,
    expanded or multiline assignments cannot grant the exception. Active
    simple assignments must be byte-for-byte identical and in the same order.
    """
    if not all(isinstance(ref, str) and re.fullmatch(r'[0-9a-f]{40}', ref) for ref in (base, head)):
        return False
    active = []
    for ref in (base, head):
        mode = subprocess.run(['git', 'ls-tree', ref, '--', '.env.example'],
                              cwd=cwd, capture_output=True, text=True)
        if mode.returncode or not re.fullmatch(r'100644 blob [0-9a-f]{40}\t\.env\.example\n', mode.stdout):
            return False
        result = subprocess.run(['git', 'show', f'{ref}:.env.example'],
                                cwd=cwd, capture_output=True)
        if result.returncode:
            return False
        try:
            content = result.stdout.decode('utf-8')
        except UnicodeError:
            return False
        if any(not (char.isprintable() or char in '\n\t') for char in content):
            return False
        lines = []
        for line in content.split('\n'):
            stripped = line.strip(' \t')
            if not stripped or stripped.startswith('#'):
                continue
            if not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*=[A-Za-z0-9_./:@%+,\-]*', stripped):
                return False
            lines.append(line)
        active.append(lines)
    return active[0] == active[1]


def mailer_roots_present(head, *, cwd=None):
    """Prove committed declarations, not runtime success or test semantics."""
    if not isinstance(head, str) or not re.fullmatch(r'[0-9a-f]{40}', head):
        return False
    listing = subprocess.run(['git', 'ls-tree', '-r', head, '--', 'internal/mailer'],
                             cwd=cwd, capture_output=True, text=True)
    if listing.returncode:
        return False
    names = []
    for line in listing.stdout.splitlines():
        metadata, separator, path = line.partition('\t')
        if not separator or not MAILER_TEST.fullmatch(path):
            continue
        if not metadata.startswith('100644 blob '):
            return False
        result = subprocess.run(['git', 'show', f'{head}:{path}'], cwd=cwd,
                                capture_output=True, text=True)
        if result.returncode:
            return False
        # Ignore complete comments/literals, not only matching grep lines. A
        # quoted sample function cannot satisfy candidate declaration presence.
        source = GO_LEXEME.sub(lambda match: (
            re.sub(r'[^\n]', ' ', match.group())
            if match.lastgroup in {'ignore', 'literal'} else match.group()), result.stdout)
        names.extend(re.findall(r'^\s*func\s+(Test[A-Za-z0-9_]+)\s*\(\s*\w+\s+\*testing\.T\s*\)\s*\{',
                                source, re.MULTILINE))
    return all(names.count(name) == 1 for name in MAILER_REQUIRED_ROOTS)


def candidate_scope(paths, base, head, *, cwd=None):
    persistence = any(p in AUTH_PERSISTENCE_FILES or p == REPOSITORY_INTERFACES for p in paths)
    return dict(mailer_roots=any(p in MAILER_FILES or MAILER_TEST.fullmatch(p) for p in paths) and mailer_roots_present(head, cwd=cwd),
                env_comments='.env.example' in paths and env_comments_only(base, head, cwd=cwd),
                aeo_roots=any(p in AEO_FILES or AEO_PATH.fullmatch(p) or p in {'cmd/main.go', REPOSITORY_INTERFACES} for p in paths) and aeo_roots_present(head, cwd=cwd),
                aeo_main='cmd/main.go' in paths and aeo_main_only(base, head, cwd=cwd),
                aeo_interface=REPOSITORY_INTERFACES in paths and aeo_interface_only(base, head, cwd=cwd),
                refresh_interface=REPOSITORY_INTERFACES in paths and refresh_interface_only(base, head, cwd=cwd),
                rotation_roots=persistence and rotation_roots_present(head, cwd=cwd))


def plan(paths, *, full=False, refresh_interface=False, rotation_roots=False, aeo_main=False, aeo_interface=False, aeo_roots=False, env_comments=False, mailer_roots=False):
    paths = sorted(set(paths))
    reasons, features, specs = [], set(), {SMOKE}
    runner_paths = application_paths = auth_paths = transaction_paths = migration_paths = other_application = False
    persistence_paths = aeo_paths = mailer_paths = False
    if full:
        reasons.append("explicit full validation")
    if not paths:
        reasons.append("no reliable change set")
    for name in paths:
        path = PurePosixPath(name)
        if (not name or "\\" in name or path.is_absolute() or ".." in path.parts
                or any(ord(c) < 32 for c in name)):
            raise ValueError("Invalid changed path")
        if (name in DOC_FILES or name.startswith("docs/") and name.endswith(".md")
                or name == '.env.example' and env_comments):
            continue
        if name in MAILER_FILES or MAILER_TEST.fullmatch(name):
            mailer_paths = application_paths = True
            continue
        if (name in AEO_FILES or AEO_PATH.fullmatch(name) or
                name == 'cmd/main.go' and aeo_main or name == REPOSITORY_INTERFACES and aeo_interface):
            aeo_paths = application_paths = True
            specs.update(AEO_SPECS)
            continue
        if name in AUTH_PERSISTENCE_FILES or name == REPOSITORY_INTERFACES and refresh_interface:
            persistence_paths = auth_paths = application_paths = True
            specs.update(AUTH_SPECS)
            continue
        if name in AUTH_FILES or AUTH_TEST.fullmatch(name):
            auth_paths = application_paths = True
            specs.update(AUTH_SPECS)
            continue
        if TRANSACTION_PATH.fullmatch(name):
            transaction_paths = application_paths = True
            continue
        if name in MIGRATION_WORKFLOW_FILES:
            migration_paths = application_paths = True
            continue
        if name.startswith(RUNNER_PREFIXES) or name in RUNNER_FILES:
            runner_paths = True
            continue
        application_paths = True
        other_application = True
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
    if aeo_paths and not aeo_roots:
        reasons.append('required AEO startup/shutdown and database roots are not proved at candidate')
    if mailer_paths and not mailer_roots:
        reasons.append('required SMTP boundary tests are absent from candidate')
    if mailer_paths and (aeo_paths or auth_paths or transaction_paths or migration_paths or other_application):
        reasons.append('mailer transport mixed with other application changes')
    if aeo_paths and (auth_paths or transaction_paths or migration_paths or other_application):
        reasons.append('AEO runtime mixed with other application changes')
    if migration_paths and (auth_paths or transaction_paths or other_application):
        reasons.append('migration workflow mixed with application changes')
    if transaction_paths and (auth_paths or other_application):
        reasons.append("transactions mixed with other application changes")
    if auth_paths and other_application:
        reasons.append('auth mixed with other application changes')
    if persistence_paths and not rotation_roots:
        reasons.append('required refresh rotation boundary roots are not proved at candidate')
    profile = ('full' if reasons else 'mailer' if mailer_paths else 'aeo' if aeo_paths else 'migration' if migration_paths else 'transaction' if transaction_paths else
               'auth-persistence' if persistence_paths else 'auth' if auth_paths else
               'runner' if runner_paths and not application_paths else 'contextual')
    return {
        "policy_version": POLICY_VERSION,
        "profile": profile,
        "reasons": sorted(set(reasons)) or [
            'SMTP transport/header/privacy and mail caller regressions plus health smoke' if profile == 'mailer'
            else 'AEO runtime, provider and persistence regressions plus health/provider browsers' if profile == 'aeo'
            else 'SQL migrations and SQLite upgrades plus health smoke' if profile == 'migration'
            else 'transaction and caller regressions plus health smoke' if profile == 'transaction'
            else 'auth/session and transaction boundaries on real databases plus auth browsers' if profile == 'auth-persistence'
            else 'targeted auth/session checks plus login, registration and health smoke' if profile == 'auth'
            else "runner checks plus health smoke" if profile == 'runner'
            else "smoke plus affected feature suites"],
        "changed_paths": paths,
        "features": sorted(features),
        "specs": [] if reasons else sorted(specs),
    }


def core_commands(configured, selected, *, explicit_full=False):
    if selected['profile'] in {'auth', 'auth-persistence', 'transaction', 'migration', 'aeo', 'mailer'}:
        check = (('python3', 'scripts/ci/check-auth.py', '--persistence') if selected['profile'] == 'auth-persistence' else
                 ('python3', 'scripts/ci/check-auth.py') if selected['profile'] == 'auth' else
                 ('python3', 'scripts/ci/check-go.py', selected['profile']))
        commands = [('make', 'verify-hygiene'), check]
        if selected['profile'] == 'auth-persistence':
            commands.append(('python3', 'scripts/ci/check-go.py', 'transaction', '--storage-only'))
        commands.append(('npm', 'ci', '--prefix', 'gocrm-ui'))
        if any(p.startswith(RUNNER_PREFIXES) or p in RUNNER_FILES for p in selected['changed_paths']):
            commands.append(('make', 'verify-runner'))
        return commands + [tuple(c) for c in configured if tuple(c) != ('make', 'verify')
                           and tuple(c) not in commands]
    if selected['profile'] == 'runner':
        commands = [('make', 'verify-hygiene'), ('make', 'verify-runner'),
                    ('npm', 'ci', '--prefix', 'gocrm-ui')]
        return commands + [tuple(c) for c in configured if tuple(c) != ('make', 'verify')
                           and tuple(c) not in commands]
    commands = [('make', 'verify')]
    if explicit_full:
        commands.append(('python3', 'scripts/ci/check-go.py', 'transaction'))
    return commands + [tuple(c) for c in configured if tuple(c) not in commands]


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
    paths = changed_paths(args.base, args.head)
    selected = plan(paths, full=args.full, **candidate_scope(paths, args.base, args.head))
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
