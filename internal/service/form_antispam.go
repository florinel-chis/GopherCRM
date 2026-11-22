package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// originAllowed matches a request origin against a form's allowlist.
//
// The match is EXACT on host[:port], case-insensitively: an entry of
// "example.com" matches an origin of "https://example.com" (browsers omit the
// default port) but neither "www.example.com" nor "example.com:8443", and an
// entry of "localhost:5173" matches only that host and port. Subdomains are
// therefore listed individually — an allowlist that silently covered every
// subdomain would not be an allowlist. An empty list allows every origin; a
// request with no origin at all matches nothing once a list exists.
func originAllowed(allowed []string, origin string) bool {
	if len(allowed) == 0 {
		return true
	}

	host := originHost(origin)
	if host == "" {
		return false
	}
	for _, entry := range allowed {
		if strings.ToLower(strings.TrimSpace(entry)) == host {
			return true
		}
	}
	return false
}

// originHost reduces an Origin (or Referer) value to its host[:port]. A bare
// host is accepted as it stands, since not every caller sends a full URL.
func originHost(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	if parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	if strings.ContainsAny(origin, "/ ") {
		return ""
	}
	return strings.ToLower(origin)
}

// checkChallenge inspects the time-trap challenge. A missing or forged
// challenge is a client fault and returns an error; a challenge that is too
// young to have been filled in by a human, or old enough to have been
// harvested, returns the spam reason instead.
func (s *formService) checkChallenge(challenge string) (string, error) {
	if strings.TrimSpace(challenge) == "" {
		return "", FieldErrors{"challenge": "the form session is missing; reload the page and try again"}
	}

	age, err := forms.ChallengeAge([]byte(s.tokenSecret), challenge, time.Now())
	if err != nil {
		return "", FieldErrors{"challenge": "the form session is invalid; reload the page and try again"}
	}
	if age < formChallengeMinAge || age > formChallengeMaxAge {
		return models.FormSpamReasonTimeTrap, nil
	}
	return "", nil
}

// checkCaptcha reports whether the captcha layer passes. A form that asks for
// the check on a server without keys skips it — documented behaviour, so a
// missing key pair degrades protection rather than blocking every submission.
// An unreachable verification service counts as a failure: it must never be
// possible to pass the check by breaking it.
//
// The call runs on the visitor's request context. When that request is
// cancelled or times out the check is abandoned and the context's error is
// returned instead of a verdict: nobody is waiting for the answer, and filing
// the submission as captcha spam would record a verdict that was never made.
func (s *formService) checkCaptcha(ctx context.Context, form *models.Form, req *PublicSubmissionRequest, meta SubmissionMeta) (bool, error) {
	if !form.CaptchaEnabled || s.verifier == nil {
		return true, nil
	}
	if strings.TrimSpace(req.CaptchaToken) == "" {
		return false, nil
	}

	ok, err := s.verifier.Verify(ctx, req.CaptchaToken, meta.IP)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, fmt.Errorf("captcha verification abandoned: %w", ctxErr)
		}
		utils.Logger.WithError(utils.LogError(err)).WithField("form_id", form.ID).
			Warn("Captcha verification failed to complete; treating the submission as spam")
		return false, nil
	}
	return ok, nil
}
