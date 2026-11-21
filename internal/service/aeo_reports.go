package service

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// ---------------------------------------------------------------- metrics ---

// aeoCounter accumulates one bucket of the visibility arithmetic: how many
// scored (non-errored) answers landed in it and how many of those mentioned the
// brand.
type aeoCounter struct {
	answers  int64
	mentions int64
}

func (s *aeoService) Dashboard(from, to time.Time) (*models.AEODashboard, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_dashboard"), "AEOService", "Dashboard")

	from, to = normalizeAEORange(from, to)

	// One projection query for the whole window; every bucket below is cut in
	// Go. No date function is spelled the same on MySQL 8 and SQLite, which is
	// why the repository never groups by day itself.
	facts, err := s.repo.ListAnswerFacts(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	profile, err := s.profileOrNil()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	brandName := ""
	if profile != nil {
		brandName = profile.BrandName
	}
	companies := aeoCompanyNames(profile, facts)

	dashboard := &models.AEODashboard{
		From:               from.Format(aeoDayFormat),
		To:                 to.Format(aeoDayFormat),
		Days:               aeoRangeDays(from, to),
		ByProvider:         []models.AEOProviderVisibility{},
		Timeline:           []models.AEOTimelinePoint{},
		ShareOfVoice:       []models.AEOShareOfVoiceEntry{},
		CompetitorTimeline: []models.AEOCompetitorTimelinePoint{},
	}

	byProvider := map[string]*aeoCounter{}
	byDay := map[string]*aeoCounter{}
	byDayProvider := map[string]map[string]*aeoCounter{}
	byCompany := map[string]int64{}
	byDayCompany := map[string]map[string]int64{}
	var scored int64

	for _, fact := range facts {
		dashboard.TotalAnswers++
		if fact.Errored {
			// A failed provider call is recorded so the run history is honest,
			// but it is not evidence of absence: it never enters a percentage.
			dashboard.FailedAnswers++
			continue
		}
		scored++

		day := fact.CreatedAt.UTC().Format(aeoDayFormat)
		aeoBump(byDay, day, fact.BrandMentioned)
		aeoBump(byProvider, fact.Provider, fact.BrandMentioned)
		if byDayProvider[day] == nil {
			byDayProvider[day] = map[string]*aeoCounter{}
		}
		aeoBump(byDayProvider[day], fact.Provider, fact.BrandMentioned)

		if byDayCompany[day] == nil {
			byDayCompany[day] = map[string]int64{}
		}
		// Mention events are counted once per answer per company, for the brand
		// and competitors alike. Counting raw occurrences would let one verbose
		// answer dominate the share-of-voice table.
		if fact.BrandMentioned && brandName != "" {
			dashboard.BrandMentions++
			byCompany[brandName]++
			byDayCompany[day][brandName]++
		}
		for name, count := range fact.CompetitorMentions {
			if count <= 0 || name == brandName {
				continue
			}
			byCompany[name]++
			byDayCompany[day][name]++
		}
	}

	// BrandMentions must stay meaningful even before a profile exists, where
	// there is no brand name to key the share-of-voice map by.
	if brandName == "" {
		for _, fact := range facts {
			if !fact.Errored && fact.BrandMentioned {
				dashboard.BrandMentions++
			}
		}
	}

	dashboard.Visibility = aeoPercent(dashboard.BrandMentions, scored)

	providerNames := make([]string, 0, len(byProvider))
	for name := range byProvider {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		counter := byProvider[name]
		dashboard.ByProvider = append(dashboard.ByProvider, models.AEOProviderVisibility{
			Provider:   name,
			Answers:    counter.answers,
			Mentions:   counter.mentions,
			Visibility: aeoPercent(counter.mentions, counter.answers),
		})
	}

	// Every day in range gets a point, including the ones with no answers, so
	// the chart shows the gap instead of interpolating across it.
	for day := aeoTruncateDay(from); day.Before(to); day = day.AddDate(0, 0, 1) {
		key := day.Format(aeoDayFormat)

		point := models.AEOTimelinePoint{Day: key, ByProvider: map[string]float64{}}
		if counter := byDay[key]; counter != nil {
			point.Overall = aeoPercent(counter.mentions, counter.answers)
		}
		for name, counter := range byDayProvider[key] {
			point.ByProvider[name] = aeoPercent(counter.mentions, counter.answers)
		}
		dashboard.Timeline = append(dashboard.Timeline, point)

		competitorPoint := models.AEOCompetitorTimelinePoint{Day: key, ByCompany: map[string]float64{}}
		if counter := byDay[key]; counter != nil && counter.answers > 0 {
			for _, company := range companies {
				competitorPoint.ByCompany[company] = aeoPercent(byDayCompany[key][company], counter.answers)
			}
		}
		dashboard.CompetitorTimeline = append(dashboard.CompetitorTimeline, competitorPoint)
	}

	var mentionEvents int64
	for _, company := range companies {
		mentionEvents += byCompany[company]
	}
	for _, company := range companies {
		mentions := byCompany[company]
		dashboard.ShareOfVoice = append(dashboard.ShareOfVoice, models.AEOShareOfVoiceEntry{
			Company:    company,
			IsBrand:    brandName != "" && company == brandName,
			Mentions:   mentions,
			Share:      aeoPercent(mentions, mentionEvents),
			Visibility: aeoPercent(mentions, scored),
		})
	}

	lastRunAt, err := s.lastRunAt()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	dashboard.LastRunAt = lastRunAt

	return dashboard, nil
}

func (s *aeoService) Citations(from, to time.Time) (*models.AEOCitationsReport, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "aeo_citations"), "AEOService", "Citations")

	from, to = normalizeAEORange(from, to)

	totalAnswers, _, err := s.repo.CountAnswersInRange(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	answersWithCitations, err := s.repo.CountAnswersWithCitations(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	rows, err := s.repo.CitationDomainStats(from, to)
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	profile, err := s.profileOrNil()
	if err != nil {
		utils.LogServiceResponse(logger, err)
		return nil, err
	}

	report := &models.AEOCitationsReport{
		From:                 from.Format(aeoDayFormat),
		To:                   to.Format(aeoDayFormat),
		TotalAnswers:         totalAnswers,
		AnswersWithCitations: answersWithCitations,
		ByCompany:            []models.AEOCitationCompanyStat{},
		TopDomains:           []models.AEOCitationDomainStat{},
	}

	brandName := ""
	if profile != nil {
		brandName = profile.BrandName
	}

	// Rates are shares of all citations in the window: the aggregation rows are
	// per-domain totals, so "citations to this company / citations to anybody"
	// is the only ratio the data supports without a second scan.
	var ownedCitations int64
	perCompany := map[string]*models.AEOCitationCompanyStat{}
	for _, row := range rows {
		report.TotalCitations += row.Citations
		if row.IsOwned {
			ownedCitations += row.Citations
		}

		company := aeoCitationCompany(row, brandName)
		if company == "" {
			// A domain that belongs to neither the brand nor a tracked
			// competitor still counts towards the totals and can still surface
			// in top_domains; it just has no company row to fold into.
			continue
		}
		stat := perCompany[company]
		if stat == nil {
			stat = &models.AEOCitationCompanyStat{Company: company, IsBrand: row.IsOwned}
			perCompany[company] = stat
		}
		stat.Citations += row.Citations
		stat.WithBrandMention += row.WithBrandMention
	}

	report.OwnedCitationRate = aeoPercent(ownedCitations, report.TotalCitations)

	// Brand first, then every tracked competitor in profile order — including
	// the ones with no citations at all, which is exactly the comparison the
	// Citations page is for.
	for _, company := range aeoCompanyNames(profile, nil) {
		stat := perCompany[company]
		if stat == nil {
			stat = &models.AEOCitationCompanyStat{Company: company, IsBrand: company == brandName && brandName != ""}
		}
		stat.IsBrand = company == brandName && brandName != ""
		stat.CitationRate = aeoPercent(stat.Citations, report.TotalCitations)
		stat.BrandMentionRate = aeoPercent(stat.WithBrandMention, stat.Citations)
		report.ByCompany = append(report.ByCompany, *stat)
	}

	domains := make([]models.AEOCitationDomainStat, 0, len(rows))
	for _, row := range rows {
		domains = append(domains, models.AEOCitationDomainStat{
			Domain:           row.Domain,
			Company:          aeoCitationCompany(row, brandName),
			IsOwned:          row.IsOwned,
			Citations:        row.Citations,
			CitationRate:     aeoPercent(row.Citations, report.TotalCitations),
			WithBrandMention: row.WithBrandMention,
			BrandMentionRate: aeoPercent(row.WithBrandMention, row.Citations),
		})
	}
	sort.SliceStable(domains, func(i, j int) bool {
		if domains[i].Citations != domains[j].Citations {
			return domains[i].Citations > domains[j].Citations
		}
		return domains[i].Domain < domains[j].Domain
	})
	if len(domains) > aeoTopDomainLimit {
		domains = domains[:aeoTopDomainLimit]
	}
	report.TopDomains = domains

	return report, nil
}

// aeoTopDomainLimit bounds the citations table so a long tail of one-off
// domains cannot bloat the response.
const aeoTopDomainLimit = 20

func aeoBump(buckets map[string]*aeoCounter, key string, mentioned bool) {
	counter := buckets[key]
	if counter == nil {
		counter = &aeoCounter{}
		buckets[key] = counter
	}
	counter.answers++
	if mentioned {
		counter.mentions++
	}
}

// aeoCompanyNames lists the companies a comparison table covers: the brand
// first, then each competitor in profile order. Before a profile exists it
// falls back to whatever competitor names the answers themselves carry, so the
// dashboard is not blank for data collected under an older profile.
func aeoCompanyNames(profile *models.AEOProfile, facts []models.AEOAnswerFact) []string {
	companies := make([]string, 0, 8)
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		companies = append(companies, name)
	}

	if profile != nil {
		add(profile.BrandName)
		for _, competitor := range profile.Competitors {
			add(competitor.Name)
		}
		return companies
	}

	extra := make([]string, 0, 8)
	for _, fact := range facts {
		for name, count := range fact.CompetitorMentions {
			if count > 0 {
				extra = append(extra, name)
			}
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		add(name)
	}
	return companies
}

// aeoCitationCompany attributes a citation row to a company: an owned domain is
// the brand, a domain carrying a competitor name is that competitor, and
// anything else is unattributed.
func aeoCitationCompany(row models.AEOCitationAggRow, brandName string) string {
	if row.IsOwned {
		return brandName
	}
	return strings.TrimSpace(row.CompetitorName)
}

// normalizeAEORange bounds a metrics window. `from` is inclusive and `to` is
// exclusive; a window wider than aeoMaxRangeDays keeps its end and loses its
// oldest days, and a degenerate window is widened to a single day so the
// per-day arithmetic always has at least one bucket.
func normalizeAEORange(from, to time.Time) (time.Time, time.Time) {
	from = from.UTC()
	to = to.UTC()

	if !to.After(from) {
		to = from.Add(24 * time.Hour)
	}
	maxWidth := time.Duration(aeoMaxRangeDays) * 24 * time.Hour
	if to.Sub(from) > maxWidth {
		from = to.Add(-maxWidth)
	}
	return from, to
}

func aeoRangeDays(from, to time.Time) int {
	days := int(math.Round(to.Sub(from).Hours() / 24))
	if days < 1 {
		days = 1
	}
	return days
}

func aeoTruncateDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// aeoPercent is the single place a ratio becomes a percentage: zero
// denominators yield 0 rather than NaN, and every result carries one decimal.
func aeoPercent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}
