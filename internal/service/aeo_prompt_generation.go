package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/florinel-chis/gophercrm/internal/aeo"
	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// GeneratePrompts asks the configured generation engine (Anthropic unless the
// administrator selects another) for buyer-style questions derived from the
// brand profile. Nothing is stored: the caller reviews the suggestions and
// POSTs the ones worth tracking to /aeo/prompts.
//
// Generation deliberately runs on ONE named engine rather than "whatever is
// configured": a silent fallback to another engine would change the shape of
// the output without the operator noticing. A missing key on the selected
// engine is therefore ErrGenerationProviderNotConfigured, which the handler
// reports as 503 PROVIDER_NOT_CONFIGURED with a message naming the engine so
// the operator knows which key to add.
func (s *aeoService) GeneratePrompts(ctx context.Context, count int) ([]string, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("count", count), "AEOService", "GeneratePrompts")

	if count <= 0 {
		count = aeoDefaultGeneratedPrompts
	}
	if count > aeoMaxGeneratedPrompts {
		count = aeoMaxGeneratedPrompts
	}

	profile, err := s.repo.GetProfile()
	if err != nil {
		if isNotFound(err) {
			return nil, apperrors.ErrProfileNotConfigured
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	provider, engine := s.generationProvider()
	if provider == nil {
		return nil, fmt.Errorf(
			"prompt generation runs on the %s engine and no %s API key is configured: %w",
			engine, engine, apperrors.ErrGenerationProviderNotConfigured)
	}

	callCtx, cancel := context.WithTimeout(ctx, aeoGenerationTimeout)
	defer cancel()

	// SafeQuery is the failure boundary: whatever the engine returns or panics
	// with — SDK errors, custom Provider errors, reflected keys or prompts —
	// leaves as a bounded *aeo.ProviderError carrying only a category and the
	// numeric HTTP status the handler maps to its response.
	answer, err := aeo.SafeQuery(callCtx, provider, buildAEOGenerationPrompt(profile, count))
	if err != nil {
		utils.LogServiceResponse(logger.WithFields(map[string]interface{}{
			"provider":         engine,
			"http_status":      aeo.ProviderHTTPStatus(err),
			"failure_category": aeo.ProviderFailureCategory(err),
		}), err)
		return nil, err
	}

	texts := parseAEOGeneratedPrompts(answer.Text, count)
	if len(texts) == 0 {
		err := fmt.Errorf("prompt generation returned no usable suggestions")
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	logger.WithField("generated", len(texts)).Info("AEO prompts generated")
	return texts, nil
}

// generationProvider returns the engine used for prompt generation together
// with its name, or a nil provider when the selected engine has no
// credentials. The selection comes from the admin configuration when a source
// is wired, defaulting to Anthropic.
func (s *aeoService) generationProvider() (aeo.Provider, string) {
	engine := aeo.ProviderAnthropic
	if s.generationEngineSource != nil {
		if v := strings.TrimSpace(s.generationEngineSource()); v != "" {
			engine = v
		}
	}
	for _, p := range s.currentProviders() {
		if p != nil && p.Name() == engine {
			return p, engine
		}
	}
	return nil, engine
}

// buildAEOGenerationPrompt renders the meta-prompt. It asks for a bare JSON
// array because that survives round-tripping far better than prose, but the
// parser tolerates a plain list as well.
func buildAEOGenerationPrompt(profile *models.AEOProfile, count int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are helping a company measure how often AI assistants mention it.\n")
	fmt.Fprintf(&b, "Brand: %s\n", profile.BrandName)
	if strings.TrimSpace(profile.Description) != "" {
		fmt.Fprintf(&b, "What it does: %s\n", strings.TrimSpace(profile.Description))
	}
	if len(profile.Competitors) > 0 {
		names := make([]string, 0, len(profile.Competitors))
		for _, c := range profile.Competitors {
			if strings.TrimSpace(c.Name) != "" {
				names = append(names, strings.TrimSpace(c.Name))
			}
		}
		if len(names) > 0 {
			fmt.Fprintf(&b, "Competitors: %s\n", strings.Join(names, ", "))
		}
	}
	fmt.Fprintf(&b, "\nWrite %d distinct questions a prospective buyer would ask an AI assistant "+
		"when researching this category. The questions must be answerable without knowing the brand, "+
		"must not name %s, and must each be under %d characters.\n",
		count, profile.BrandName, aeoPromptTextMaxLength)
	fmt.Fprintf(&b, "Reply with a JSON array of strings and nothing else.")

	return b.String()
}

// parseAEOGeneratedPrompts turns a model reply into clean prompt texts. It
// accepts a JSON array (the requested shape) and falls back to one-per-line
// parsing, stripping bullets, numbering and quoting. Duplicates are dropped
// case-insensitively so the caller does not immediately hit ErrDuplicatePrompt.
func parseAEOGeneratedPrompts(raw string, limit int) []string {
	candidates := decodeAEOPromptArray(raw)
	if len(candidates) == 0 {
		candidates = strings.Split(raw, "\n")
	}

	seen := make(map[string]bool, len(candidates))
	texts := make([]string, 0, limit)
	for _, candidate := range candidates {
		text := cleanGeneratedPromptText(candidate)
		if text == "" || len([]rune(text)) > aeoPromptTextMaxLength {
			continue
		}
		key := strings.ToLower(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		texts = append(texts, text)
		if len(texts) == limit {
			break
		}
	}
	return texts
}

// decodeAEOPromptArray extracts the outermost JSON array from a reply that may
// be wrapped in prose or a fenced code block. A decode failure is not an error:
// the caller falls back to line parsing.
func decodeAEOPromptArray(raw string) []string {
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		return nil
	}
	return out
}

// cleanGeneratedPromptText strips list decoration from one candidate line and
// returns "" for anything too short to be a real question.
func cleanGeneratedPromptText(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.TrimLeft(text, "-*•\t ")
	// Leading "1." / "1)" numbering, but only when the digits are actually
	// followed by a separator: "5 best CRMs?" must survive intact.
	if idx := strings.IndexFunc(text, func(r rune) bool { return !unicode.IsDigit(r) }); idx > 0 {
		if rest := text[idx:]; strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, ")") {
			text = rest[1:]
		}
	}
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `"'`)
	text = strings.TrimRight(text, ",")
	text = strings.TrimSpace(text)

	if len([]rune(text)) < aeoMinGeneratedPromptLength {
		return ""
	}
	return text
}
