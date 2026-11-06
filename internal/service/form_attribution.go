package service

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/florinel-chis/gophercrm/internal/models"
)

// Attribution of a form enquiry (#220): the five utm_* values and the host of
// the page the form was submitted from, stored on the submission's own row and
// kept apart from the lead's source. The parsing is deterministic and keeps as
// little as it can: a value is normalised and clipped, a URL contributes its
// host alone, and anything that does not parse contributes nothing.
//
// It is applied where a received submission is stored and where a double
// opt-in confirmation finalises one, never in newSubmission, which recordSpam
// shares: a spam row keeps empty attribution whatever it carried.

const (
	// formAttributionValueMaxRunes is the width of the utm_* columns.
	formAttributionValueMaxRunes = 100
	// formReferrerHostMaxLength is the width of form_submissions.referrer_host.
	formReferrerHostMaxLength = 255
)

// formAttribution is the parsed attribution of one submission.
type formAttribution struct {
	UTMSource    string
	UTMMedium    string
	UTMCampaign  string
	UTMTerm      string
	UTMContent   string
	ReferrerHost string
}

// parseFormAttribution derives the attribution from the submitted values and
// the page_url the embed script reported. For each utm_* key a non-blank
// submitted field of that name wins; otherwise the first value of that query
// parameter of an absolute http or https page_url is used. referrer_host is
// the lower-cased host of such a page_url, without port, credentials, path or
// query.
func parseFormAttribution(values map[string]string, pageURL string) formAttribution {
	page := parseAttributionURL(pageURL)
	var query url.Values
	if page != nil {
		query = page.Query()
	}
	pick := func(key string) string {
		if submitted := normaliseAttributionValue(values[key]); submitted != "" {
			return submitted
		}
		if candidates := query[key]; len(candidates) > 0 {
			return normaliseAttributionValue(candidates[0])
		}
		return ""
	}
	attribution := formAttribution{
		UTMSource:   pick("utm_source"),
		UTMMedium:   pick("utm_medium"),
		UTMCampaign: pick("utm_campaign"),
		UTMTerm:     pick("utm_term"),
		UTMContent:  pick("utm_content"),
	}
	if page != nil {
		attribution.ReferrerHost = referrerHost(page)
	}
	return attribution
}

// parseAttributionURL returns the page URL when it is an absolute http or
// https URL with a host, and nil for anything else.
func parseAttributionURL(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	page, err := url.Parse(raw)
	if err != nil || page == nil {
		return nil
	}
	scheme := strings.ToLower(page.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil
	}
	if page.Host == "" || page.Hostname() == "" {
		return nil
	}
	return page
}

// referrerHost is the lower-cased host of the page, without port or
// credentials, cut to the column width on a rune boundary.
func referrerHost(page *url.URL) string {
	host := strings.ToLower(stripAttributionControls(page.Hostname()))
	return truncateRunes(strings.TrimSpace(host), formReferrerHostMaxLength)
}

// normaliseAttributionValue removes invalid UTF-8 and control characters,
// trims, lower-cases and cuts the value to 100 runes on a rune boundary.
func normaliseAttributionValue(value string) string {
	value = strings.TrimSpace(stripAttributionControls(value))
	value = strings.ToLower(value)
	return truncateRunes(value, formAttributionValueMaxRunes)
}

// stripAttributionControls drops invalid UTF-8 and every control character.
func stripAttributionControls(value string) string {
	value = strings.ToValidUTF8(value, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

// applyFormAttribution sets the six attribution columns of a finalised
// submission from its stored values and its stored page URL (the referrer).
// Both the received and the confirmation path call it, so a double opt-in
// submission is attributed exactly as it would have been on arrival.
func applyFormAttribution(submission *models.FormSubmission) {
	attribution := parseFormAttribution(submission.Data, submission.Referrer)
	submission.UTMSource = attribution.UTMSource
	submission.UTMMedium = attribution.UTMMedium
	submission.UTMCampaign = attribution.UTMCampaign
	submission.UTMTerm = attribution.UTMTerm
	submission.UTMContent = attribution.UTMContent
	submission.ReferrerHost = attribution.ReferrerHost
}

// attributionFromSubmission reads the stored attribution back, for the
// automation conditions.
func attributionFromSubmission(submission *models.FormSubmission) *formAttribution {
	if submission == nil {
		return nil
	}
	return &formAttribution{
		UTMSource:    submission.UTMSource,
		UTMMedium:    submission.UTMMedium,
		UTMCampaign:  submission.UTMCampaign,
		UTMTerm:      submission.UTMTerm,
		UTMContent:   submission.UTMContent,
		ReferrerHost: submission.ReferrerHost,
	}
}

// value returns the attribution field named by a condition; nil reads empty.
func (a *formAttribution) value(field string) string {
	if a == nil {
		return ""
	}
	switch field {
	case "utm_source":
		return a.UTMSource
	case "utm_medium":
		return a.UTMMedium
	case "utm_campaign":
		return a.UTMCampaign
	case "utm_term":
		return a.UTMTerm
	case "utm_content":
		return a.UTMContent
	case "referrer_host":
		return a.ReferrerHost
	}
	return ""
}
