package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDisableRateLimitDocsMatchCode pins the guides that describe
// DISABLE_RATE_LIMIT to what the code does (#775). The switch is read in
// internal/middleware/rate_limit.go only: RateLimitStrict returns a
// pass-through, NewSCIMRateLimits returns nil (the SCIM token limit and the
// SCIM refusal limit), and CheckRateLimitSwitch refuses SERVER_MODE=production.
// The moderate and generous tiers stay on. So no page may narrow the switch to
// the strict tier, every page that describes it names the strict tier and the
// SCIM limits, and the production refusal stays documented. CHANGELOG.md is
// history and keeps its old wording. Text is matched with whitespace
// collapsed, so a line wrap cannot hide a phrase.
func TestDisableRateLimitDocsMatchCode(t *testing.T) {
	root := repositoryRoot(t)

	t.Run("code", func(t *testing.T) {
		readers := disableRateLimitReaders(t, root)
		assert.Equal(t, []string{
			"internal/middleware/rate_limit.go: CheckRateLimitSwitch",
			"internal/middleware/rate_limit.go: NewSCIMRateLimits",
			"internal/middleware/rate_limit.go: RateLimitStrict",
		}, readers, "the functions that read the \"DISABLE_RATE_LIMIT\" literal changed: update the guides and this test")

		mainGo := readRepositoryFile(t, "cmd/main.go")
		for _, call := range []string{
			"middleware.NewSCIMRateLimits(",
			"middleware.RateLimitStrict()",
			"middleware.CheckRateLimitSwitch(",
		} {
			assert.Contains(t, mainGo, call, "cmd/main.go no longer calls %s", call)
		}
	})

	t.Run("no page narrows it to the strict tier", func(t *testing.T) {
		narrowing := regexp.MustCompile("(?i)\\bonly(\\*\\*)? (bypasses |lifts )?(the )?strict\\b|\\bstrict( `/auth`)? tier (only|alone)\\b|\\bstrict-only\\b|\\bstrict-tier setting\\b|\\bauth tier only\\b")
		pages := trackedMarkdownPages(t, root)
		require.NotEmpty(t, pages)
		pages = append(pages, ".env.example")
		for _, page := range pages {
			if page == "CHANGELOG.md" {
				continue
			}
			text := collapseWhitespace(readRepositoryFile(t, page))
			for _, match := range narrowing.FindAllString(text, -1) {
				t.Errorf("%s narrows DISABLE_RATE_LIMIT to the strict tier: %q", page, match)
			}
		}
	})

	t.Run("every page names the strict tier and the SCIM limits", func(t *testing.T) {
		lifts := regexp.MustCompile(`\b(lifts?|bypass(es)?)\b`)
		for _, page := range []string{
			"docs/KNOWN_LIMITATIONS.md",
			"docs/FEATURES.md",
			"docs/AUTHORIZATION.md",
			"docs/DEVELOPMENT.md",
			"docs/CONFIGURATION.md",
			"docs/testing/01-authentication.md",
			"docs/testing/09-cross-cutting.md",
			"docs/testing/11-aeo.md",
			"docs/testing/README.md",
			"gocrm-ui/e2e/README.md",
		} {
			named := false
			for _, sentence := range docSentences(readRepositoryFile(t, page)) {
				loc := disableRateLimitVar.FindStringIndex(sentence)
				if loc == nil {
					continue
				}
				hasSCIM := strings.Contains(sentence, "SCIM")
				if hasSCIM && strings.Contains(strings.ToLower(sentence), "strict") {
					named = true
				}
				if lifts.MatchString(sentence[loc[1]:]) && !hasSCIM {
					t.Errorf("%s: a sentence says what DISABLE_RATE_LIMIT lifts without naming the SCIM limits: %q", page, sentence)
				}
			}
			assert.True(t, named, "%s has no sentence naming DISABLE_RATE_LIMIT, the strict tier and the SCIM limits", page)
		}
	})

	t.Run("the production refusal is kept", func(t *testing.T) {
		for _, page := range []string{
			"docs/KNOWN_LIMITATIONS.md",
			"docs/FEATURES.md",
			"docs/AUTHORIZATION.md",
			"docs/DEVELOPMENT.md",
			"docs/CONFIGURATION.md",
		} {
			found := false
			for _, sentence := range docSentences(readRepositoryFile(t, page)) {
				if disableRateLimitVar.MatchString(sentence) && strings.Contains(sentence, "SERVER_MODE=production") {
					found = true
					break
				}
			}
			assert.True(t, found, "%s no longer says that DISABLE_RATE_LIMIT stops startup with SERVER_MODE=production", page)
		}
	})
}

// disableRateLimitVar matches the variable but not GOPHERCRM_E2E_DISABLE_RATE_LIMIT.
var disableRateLimitVar = regexp.MustCompile(`(^|[^_A-Z])DISABLE_RATE_LIMIT`)

var whitespaceRun = regexp.MustCompile(`\s+`)

func collapseWhitespace(text string) string {
	return whitespaceRun.ReplaceAllString(text, " ")
}

// sentenceBreak finds a sentence end: '.', ';' or a table '|' followed by
// whitespace. The punctuation stays with the sentence it ends.
var sentenceBreak = regexp.MustCompile(`[.;|]\s`)

func docSentences(text string) []string {
	text = collapseWhitespace(text)
	var sentences []string
	start := 0
	for _, loc := range sentenceBreak.FindAllStringIndex(text, -1) {
		sentences = append(sentences, text[start:loc[0]+1])
		start = loc[1]
	}
	return append(sentences, text[start:])
}

func trackedMarkdownPages(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "*.md")
	cmd.Dir = root
	out, err := cmd.Output()
	require.NoError(t, err, "git ls-files")
	var pages []string
	for _, page := range strings.Split(string(out), "\x00") {
		if page != "" {
			pages = append(pages, page)
		}
	}
	return pages
}

// disableRateLimitReaders lists "<file>: <function>" for every non-test Go
// source under cmd/ and internal/ whose function body holds the string literal
// "DISABLE_RATE_LIMIT" exactly; a literal outside any function is listed as
// "<file>: <package level>".
func disableRateLimitReaders(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, tree := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			inFunc := map[*ast.BasicLit]bool{}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok && isDisableRateLimitLiteral(lit) {
						inFunc[lit] = true
						seen[rel+": "+fn.Name.Name] = true
					}
					return true
				})
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && isDisableRateLimitLiteral(lit) && !inFunc[lit] {
					seen[rel+": <package level>"] = true
				}
				return true
			})
			return nil
		})
		require.NoError(t, err, "walk %s", tree)
	}
	readers := make([]string, 0, len(seen))
	for reader := range seen {
		readers = append(readers, reader)
	}
	sort.Strings(readers)
	return readers
}

func isDisableRateLimitLiteral(lit *ast.BasicLit) bool {
	if lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	return err == nil && value == "DISABLE_RATE_LIMIT"
}
