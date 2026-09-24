package aeo

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sirupsen/logrus"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// urlPattern matches bare http(s) URLs inside prose. Closing brackets and
// quotes terminate the match because answers routinely wrap links in markdown
// or parentheses.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// urlTrailingPunctuation is stripped from the tail of an extracted URL: a link
// at the end of a sentence swallows the full stop otherwise.
const urlTrailingPunctuation = `.,;:)]`

// MentionResult is the outcome of scanning one answer for brand and competitor
// mentions.
type MentionResult struct {
	// BrandMentioned is true when the brand name or any of its aliases occurs.
	BrandMentioned bool
	// FirstMentionPos is the rune index of the earliest brand hit, -1 when the
	// brand is absent. Rune, not byte, so the value stays meaningful for
	// non-ASCII answers.
	FirstMentionPos int
	// CompetitorMentions maps competitor name to occurrence count. Competitors
	// with no occurrences are omitted.
	CompetitorMentions map[string]int
}

// DetectMentions scans an answer for the brand (name + aliases) and for every
// competitor (name + aliases). Matching is case-insensitive and bounded by
// unicode word boundaries, so "CRM" does not match "CRMs" and "Acme" does not
// match "Acmerica".
func DetectMentions(text string, profile *models.AEOProfile) MentionResult {
	result := MentionResult{
		FirstMentionPos:    -1,
		CompetitorMentions: map[string]int{},
	}
	if profile == nil || text == "" {
		return result
	}

	haystack := lowerRunes(text)

	brandTerms := append([]string{profile.BrandName}, profile.BrandAliases...)
	for _, term := range brandTerms {
		count, pos := countTermInRunes(haystack, term)
		if count == 0 {
			continue
		}
		result.BrandMentioned = true
		if result.FirstMentionPos == -1 || pos < result.FirstMentionPos {
			result.FirstMentionPos = pos
		}
	}

	for _, competitor := range profile.Competitors {
		name := strings.TrimSpace(competitor.Name)
		if name == "" {
			continue
		}
		total := 0
		// A competitor's own name and its aliases are counted together: they
		// all denote the same company.
		for _, term := range append([]string{competitor.Name}, competitor.Aliases...) {
			count, _ := countTermInRunes(haystack, term)
			total += count
		}
		if total > 0 {
			result.CompetitorMentions[name] += total
		}
	}

	return result
}

// CountTerm reports how often term occurs in text as a whole word, and the rune
// index of the first occurrence (-1 when absent). Matching is case-insensitive.
func CountTerm(text, term string) (count, firstPos int) {
	return countTermInRunes(lowerRunes(text), term)
}

// countTermInRunes is the shared implementation. The haystack is pre-lowered
// once per answer so scanning N terms does not re-lower the text N times.
//
// Lowercasing is done rune by rune rather than with strings.ToLower because the
// latter can change the length of the string (some runes lower-case to multiple
// runes), which would desynchronize the reported index from the original text.
func countTermInRunes(haystack []rune, term string) (count, firstPos int) {
	firstPos = -1

	needle := lowerRunes(strings.TrimSpace(term))
	if len(needle) == 0 || len(haystack) < len(needle) {
		return 0, -1
	}

	for i := 0; i+len(needle) <= len(haystack); i++ {
		if !runesEqualAt(haystack, needle, i) {
			continue
		}
		if !isWordBoundary(haystack, i, i+len(needle)) {
			continue
		}
		count++
		if firstPos == -1 {
			firstPos = i
		}
		// Skip past this occurrence: overlapping matches of the same term
		// would double-count.
		i += len(needle) - 1
	}

	return count, firstPos
}

func runesEqualAt(haystack, needle []rune, offset int) bool {
	for j, r := range needle {
		if haystack[offset+j] != r {
			return false
		}
	}
	return true
}

// isWordBoundary reports whether the runes flanking [start,end) are non-word
// runes. Go's regexp \b is ASCII-only, which would mis-handle "Ştefan" and
// "café"; unicode.IsLetter/IsDigit does not.
func isWordBoundary(haystack []rune, start, end int) bool {
	if start > 0 && isWordRune(haystack[start-1]) {
		return false
	}
	if end < len(haystack) && isWordRune(haystack[end]) {
		return false
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// lowerRunes lowercases rune by rune, preserving the rune count so indices in
// the lowered slice map straight back onto the original text.
func lowerRunes(s string) []rune {
	runes := []rune(s)
	for i, r := range runes {
		runes[i] = unicode.ToLower(r)
	}
	return runes
}

// ExtractCitations collects the sources an answer points at: the URLs the
// provider supplied natively (Perplexity) plus every URL found in the prose.
// Each is normalized to a domain and classified as owned, competitor-owned or
// neither. Citations are returned without AnswerID; the engine sets it when the
// answer row is persisted.
func ExtractCitations(text string, native []string, profile *models.AEOProfile) []models.AEOCitation {
	return extractCitations(text, native, profile, logProvider())
}

// extractCitations is ExtractCitations with the logger supplied by the caller,
// so the engine can tag skips with the run, prompt and provider.
//
// A URL or host too long for its aeo_citations column is skipped and logged,
// never truncated: a cut URL points somewhere else, and on MySQL one oversized
// value would fail the insert and roll back the whole answer with it. The log
// line carries the domain and the length but not the URL, whose query string
// may hold a token.
func extractCitations(text string, native []string, profile *models.AEOProfile, log *logrus.Entry) []models.AEOCitation {
	rawURLs := make([]string, 0, len(native)+4)
	rawURLs = append(rawURLs, native...)
	rawURLs = append(rawURLs, urlPattern.FindAllString(text, -1)...)

	owned, competitorByDomain := domainIndex(profile, log)

	citations := make([]models.AEOCitation, 0, len(rawURLs))
	seen := make(map[string]struct{}, len(rawURLs))

	for _, raw := range rawURLs {
		cleaned := strings.TrimRight(strings.TrimSpace(raw), urlTrailingPunctuation)
		if cleaned == "" {
			continue
		}
		if _, duplicate := seen[cleaned]; duplicate {
			continue
		}
		seen[cleaned] = struct{}{}

		domain := NormalizeDomain(cleaned)
		if domain == "" {
			continue
		}
		if length := utf8.RuneCountInString(domain); length > models.AEOCitationDomainMaxLength {
			log.WithFields(logrus.Fields{
				"domain_length": length,
				"max_length":    models.AEOCitationDomainMaxLength,
			}).Warn("skipping AEO citation: host is too long to store")
			continue
		}
		if length := utf8.RuneCountInString(cleaned); length > models.AEOCitationURLMaxLength {
			log.WithFields(logrus.Fields{
				"domain":     domain,
				"url_length": length,
				"max_length": models.AEOCitationURLMaxLength,
			}).Warn("skipping AEO citation: URL is too long to store")
			continue
		}

		citation := models.AEOCitation{
			URL:    cleaned,
			Domain: domain,
		}
		if matchesAnyDomain(domain, owned) {
			citation.IsOwned = true
		}
		for competitorDomain, name := range competitorByDomain {
			if domainMatches(domain, competitorDomain) {
				citation.CompetitorName = name
				break
			}
		}
		citations = append(citations, citation)
	}

	return citations
}

// NormalizeDomain reduces a URL to its bare host: lowercased, port removed and
// a leading "www." stripped. It returns "" for anything that is not a parseable
// absolute URL with a host.
func NormalizeDomain(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return ""
	}
	return strings.TrimPrefix(host, "www.")
}

// domainIndex normalizes the profile's owned domains and the competitors'
// domains once per answer. A competitor whose name does not fit the
// competitor_name column is left out of the index, so its citations are still
// stored, just without the attribution. The API rejects such names; this only
// guards profiles saved before it did.
func domainIndex(profile *models.AEOProfile, log *logrus.Entry) (owned []string, competitorByDomain map[string]string) {
	competitorByDomain = map[string]string{}
	if profile == nil {
		return nil, competitorByDomain
	}

	for _, domain := range profile.OwnedDomains {
		if normalized := normalizeBareDomain(domain); normalized != "" {
			owned = append(owned, normalized)
		}
	}
	for _, competitor := range profile.Competitors {
		normalized := normalizeBareDomain(competitor.Domain)
		name := strings.TrimSpace(competitor.Name)
		if normalized == "" || name == "" {
			continue
		}
		if length := utf8.RuneCountInString(name); length > models.AEOCompetitorNameMaxLength {
			log.WithFields(logrus.Fields{
				"competitor_domain": normalized,
				"name_length":       length,
				"max_length":        models.AEOCompetitorNameMaxLength,
			}).Warn("not attributing AEO citations to competitor: name is too long to store")
			continue
		}
		if _, exists := competitorByDomain[normalized]; !exists {
			competitorByDomain[normalized] = name
		}
	}
	return owned, competitorByDomain
}

// normalizeBareDomain accepts either a bare domain ("acme.com") or a full URL
// and reduces both to the same normalized host.
func normalizeBareDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return NormalizeDomain(value)
	}
	value = strings.TrimPrefix(value, "www.")
	value = strings.TrimSuffix(value, "/")
	if idx := strings.IndexAny(value, "/:"); idx >= 0 {
		value = value[:idx]
	}
	return value
}

func matchesAnyDomain(domain string, candidates []string) bool {
	for _, candidate := range candidates {
		if domainMatches(domain, candidate) {
			return true
		}
	}
	return false
}

// domainMatches treats a subdomain as belonging to its parent, so a citation of
// blog.acme.com counts as a citation of the owned domain acme.com.
func domainMatches(domain, candidate string) bool {
	if candidate == "" {
		return false
	}
	return domain == candidate || strings.HasSuffix(domain, "."+candidate)
}
