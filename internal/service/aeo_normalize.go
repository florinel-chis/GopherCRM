package service

import (
	"fmt"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func normalizeAEOPromptText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", ErrAEOInvalidPrompt
	}
	if len([]rune(text)) > aeoPromptTextMaxLength {
		return "", fmt.Errorf("prompt text is longer than %d characters: %w", aeoPromptTextMaxLength, ErrAEOInvalidPrompt)
	}
	return text, nil
}

// normalizeAEOProfile trims and de-duplicates the profile in place so the row
// that is stored is exactly the row that was validated, and pins the singleton
// primary key.
func normalizeAEOProfile(profile *models.AEOProfile) error {
	profile.BrandName = strings.TrimSpace(profile.BrandName)
	if profile.BrandName == "" {
		return fmt.Errorf("brand name is required: %w", ErrAEOInvalidProfile)
	}
	if len([]rune(profile.BrandName)) > aeoBrandNameMaxLength {
		return fmt.Errorf("brand name is longer than %d characters: %w", aeoBrandNameMaxLength, ErrAEOInvalidProfile)
	}

	profile.Description = strings.TrimSpace(profile.Description)
	if len([]rune(profile.Description)) > aeoDescriptionMaxLength {
		return fmt.Errorf("description is longer than %d characters: %w", aeoDescriptionMaxLength, ErrAEOInvalidProfile)
	}

	profile.BrandAliases = aeoDedupeStrings(profile.BrandAliases, false)
	profile.OwnedDomains = aeoDedupeStrings(profile.OwnedDomains, true)

	competitors := make([]models.AEOCompetitor, 0, len(profile.Competitors))
	for _, competitor := range profile.Competitors {
		competitor.Name = strings.TrimSpace(competitor.Name)
		if competitor.Name == "" {
			continue
		}
		if len([]rune(competitor.Name)) > aeoCompetitorNameMaxLength {
			return fmt.Errorf("competitor name is longer than %d characters: %w", aeoCompetitorNameMaxLength, ErrAEOInvalidProfile)
		}
		competitor.Aliases = aeoDedupeStrings(competitor.Aliases, false)
		competitor.Domain = aeoNormalizeProfileDomain(competitor.Domain)
		competitors = append(competitors, competitor)
	}
	profile.Competitors = competitors

	// Single row by construction: the service never inserts a second profile.
	profile.ID = aeoProfileID
	return nil
}

// aeoDedupeStrings trims, drops blanks and removes case-insensitive duplicates
// while preserving order. With asDomain set, each entry is also normalized to a
// bare lowercase host so "https://WWW.Acme.com/" and "acme.com" collapse.
func aeoDedupeStrings(values []string, asDomain bool) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if asDomain {
			value = aeoNormalizeProfileDomain(value)
		} else {
			value = strings.TrimSpace(value)
		}
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

// aeoNormalizeProfileDomain reduces whatever the user typed to a bare host:
// lowercase, no scheme, no path, no leading "www.". It accepts a bare domain,
// which url.Parse does not, so it is deliberately a string operation rather
// than a URL parse.
func aeoNormalizeProfileDomain(raw string) string {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return ""
	}
	if idx := strings.Index(domain, "://"); idx >= 0 {
		domain = domain[idx+3:]
	}
	if idx := strings.IndexAny(domain, "/?#"); idx >= 0 {
		domain = domain[:idx]
	}
	if idx := strings.Index(domain, "@"); idx >= 0 {
		domain = domain[idx+1:]
	}
	domain = strings.TrimPrefix(domain, "www.")
	return strings.Trim(domain, ".")
}
