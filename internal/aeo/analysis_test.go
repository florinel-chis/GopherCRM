package aeo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/florinel-chis/gophercrm/internal/models"
)

func TestCountTerm(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		term      string
		wantCount int
		wantPos   int
	}{
		{
			name:      "simple hit",
			text:      "We recommend Acme for that.",
			term:      "Acme",
			wantCount: 1,
			wantPos:   13,
		},
		{
			name:      "case insensitive both directions",
			text:      "ACME and acme and AcMe",
			term:      "aCmE",
			wantCount: 3,
			wantPos:   0,
		},
		{
			name:      "substring trap: Acme inside Acmerica",
			text:      "Acmerica is a different company",
			term:      "Acme",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "plural fails the trailing boundary",
			text:      "Several CRMs exist",
			term:      "CRM",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "singular passes the trailing boundary",
			text:      "Several CRM tools exist",
			term:      "CRM",
			wantCount: 1,
			wantPos:   8,
		},
		{
			name:      "leading boundary respected",
			text:      "microCRM is not CRM",
			term:      "CRM",
			wantCount: 1,
			wantPos:   16,
		},
		{
			name:      "punctuation is a boundary",
			text:      "Try Acme, or Acme. Or (Acme)!",
			term:      "acme",
			wantCount: 3,
			wantPos:   4,
		},
		{
			name:      "digits are word runes",
			text:      "Acme2 is not Acme",
			term:      "Acme",
			wantCount: 1,
			wantPos:   13,
		},
		{
			name:      "underscore is a word rune",
			text:      "acme_crm and acme",
			term:      "acme",
			wantCount: 1,
			wantPos:   13,
		},
		{
			name:      "multi-word term",
			text:      "Ask Acme Corp about it, Acme Corp knows",
			term:      "Acme Corp",
			wantCount: 2,
			wantPos:   4,
		},
		{
			name:      "unicode romanian diacritic term",
			text:      "Recomandăm Ştefan Consulting pentru asta.",
			term:      "Ştefan",
			wantCount: 1,
			wantPos:   11,
		},
		{
			name:      "unicode accented term is not matched by its ascii form",
			text:      "The café is closed",
			term:      "cafe",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "unicode accented term matches itself",
			text:      "The café is closed",
			term:      "café",
			wantCount: 1,
			wantPos:   4,
		},
		{
			name:      "unicode accented term respects the trailing boundary",
			text:      "Two cafés downtown",
			term:      "café",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "position is a rune index, not a byte index",
			text:      "Ştefan spune că Acme e bun",
			term:      "Acme",
			wantCount: 1,
			wantPos:   16,
		},
		{
			// A naive scan would find three overlapping matches at rune 0, 3
			// and 6; counting must advance past each hit instead.
			name:      "overlapping occurrences are not double counted",
			text:      "ha ha ha ha",
			term:      "ha ha",
			wantCount: 2,
			wantPos:   0,
		},
		{
			name:      "repeated term inside a word is still rejected",
			text:      "aaaa",
			term:      "aa",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "blank term is skipped",
			text:      "anything at all",
			term:      "   ",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "empty term is skipped",
			text:      "anything at all",
			term:      "",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "term longer than the text",
			text:      "hi",
			term:      "hello there",
			wantCount: 0,
			wantPos:   -1,
		},
		{
			name:      "empty text",
			text:      "",
			term:      "Acme",
			wantCount: 0,
			wantPos:   -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			count, pos := CountTerm(tc.text, tc.term)
			assert.Equal(t, tc.wantCount, count, "count")
			assert.Equal(t, tc.wantPos, pos, "first position")
		})
	}
}

func testProfile() *models.AEOProfile {
	return &models.AEOProfile{
		BrandName:    "Acme",
		BrandAliases: []string{"Acme Inc", "AcmeCRM"},
		OwnedDomains: []string{"acme.com", "acme.io"},
		Competitors: []models.AEOCompetitor{
			{Name: "Globex", Aliases: []string{"Globex Corp"}, Domain: "globex.com"},
			{Name: "Initech", Aliases: nil, Domain: "initech.example"},
		},
	}
}

func TestDetectMentions(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		profile         *models.AEOProfile
		wantBrand       bool
		wantFirstPos    int
		wantCompetitors map[string]int
	}{
		{
			name:            "brand by primary name",
			text:            "I would pick Acme for a small team.",
			profile:         testProfile(),
			wantBrand:       true,
			wantFirstPos:    13,
			wantCompetitors: map[string]int{},
		},
		{
			name:            "brand by alias only",
			text:            "AcmeCRM has a good free tier.",
			profile:         testProfile(),
			wantBrand:       true,
			wantFirstPos:    0,
			wantCompetitors: map[string]int{},
		},
		{
			name: "earliest hit wins across name and aliases",
			// "Acme Inc" starts at 0 and "Acme" also matches there; the
			// earliest rune index must be reported either way.
			text:            "Acme Inc is fine, and Acme is the same company.",
			profile:         testProfile(),
			wantBrand:       true,
			wantFirstPos:    0,
			wantCompetitors: map[string]int{},
		},
		{
			name:            "no brand mention",
			text:            "Globex and Initech are the usual answers.",
			profile:         testProfile(),
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{"Globex": 1, "Initech": 1},
		},
		{
			name:            "competitor name and alias count towards the same company",
			text:            "Globex is popular; Globex Corp especially.",
			profile:         testProfile(),
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{"Globex": 3},
		},
		{
			name:            "brand and competitors together",
			text:            "Acme beats Globex on price. Initech is cheaper still.",
			profile:         testProfile(),
			wantBrand:       true,
			wantFirstPos:    0,
			wantCompetitors: map[string]int{"Globex": 1, "Initech": 1},
		},
		{
			name:            "substring traps ignored for brand and competitors",
			text:            "Acmerica competes with Globexity.",
			profile:         testProfile(),
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{},
		},
		{
			name:            "empty profile matches nothing",
			text:            "Acme is great",
			profile:         &models.AEOProfile{},
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{},
		},
		{
			name:            "nil profile is safe",
			text:            "Acme is great",
			profile:         nil,
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{},
		},
		{
			name:            "empty answer text",
			text:            "",
			profile:         testProfile(),
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{},
		},
		{
			name: "competitor with a blank name is skipped",
			text: "Nobody mentions anything useful.",
			profile: &models.AEOProfile{
				BrandName:   "Acme",
				Competitors: []models.AEOCompetitor{{Name: "  ", Aliases: []string{"anything"}}},
			},
			wantBrand:       false,
			wantFirstPos:    -1,
			wantCompetitors: map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectMentions(tc.text, tc.profile)
			assert.Equal(t, tc.wantBrand, got.BrandMentioned, "brand mentioned")
			assert.Equal(t, tc.wantFirstPos, got.FirstMentionPos, "first mention position")
			assert.Equal(t, tc.wantCompetitors, got.CompetitorMentions, "competitor mentions")
			assert.NotNil(t, got.CompetitorMentions, "map must never be nil so it serializes as {}")
		})
	}
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain host", in: "https://acme.com/pricing", want: "acme.com"},
		{name: "www stripped", in: "https://www.acme.com/", want: "acme.com"},
		{name: "uppercase host lowered", in: "HTTPS://WWW.ACME.COM/Path", want: "acme.com"},
		{name: "port removed", in: "http://acme.com:8080/x", want: "acme.com"},
		{name: "subdomain preserved", in: "https://blog.acme.com/post", want: "blog.acme.com"},
		{name: "http scheme", in: "http://acme.io", want: "acme.io"},
		{name: "no scheme is not an absolute url", in: "acme.com/pricing", want: ""},
		{name: "empty string", in: "", want: ""},
		{name: "whitespace only", in: "   ", want: ""},
		{name: "unparseable url", in: "https://ac me.com/%zz", want: ""},
		{name: "scheme without host", in: "mailto:hi@acme.com", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NormalizeDomain(tc.in))
		})
	}
}

func TestExtractCitations(t *testing.T) {
	profile := testProfile()

	t.Run("urls from prose are classified", func(t *testing.T) {
		text := "See https://www.acme.com/pricing and https://globex.com/compare, " +
			"plus https://news.example.org/story."
		got := ExtractCitations(text, nil, profile)

		require.Len(t, got, 3)

		assert.Equal(t, "https://www.acme.com/pricing", got[0].URL)
		assert.Equal(t, "acme.com", got[0].Domain)
		assert.True(t, got[0].IsOwned)
		assert.Empty(t, got[0].CompetitorName)

		assert.Equal(t, "https://globex.com/compare", got[1].URL)
		assert.Equal(t, "globex.com", got[1].Domain)
		assert.False(t, got[1].IsOwned)
		assert.Equal(t, "Globex", got[1].CompetitorName)

		// The sentence-ending full stop must not become part of the URL.
		assert.Equal(t, "https://news.example.org/story", got[2].URL)
		assert.Equal(t, "news.example.org", got[2].Domain)
		assert.False(t, got[2].IsOwned)
		assert.Empty(t, got[2].CompetitorName)
	})

	t.Run("subdomains of an owned domain are owned", func(t *testing.T) {
		got := ExtractCitations("https://blog.acme.com/post", nil, profile)
		require.Len(t, got, 1)
		assert.Equal(t, "blog.acme.com", got[0].Domain)
		assert.True(t, got[0].IsOwned)
	})

	t.Run("native citations are merged ahead of prose urls", func(t *testing.T) {
		got := ExtractCitations(
			"Sources: https://globex.com/a",
			[]string{"https://acme.io/whitepaper"},
			profile,
		)
		require.Len(t, got, 2)
		assert.Equal(t, "https://acme.io/whitepaper", got[0].URL)
		assert.True(t, got[0].IsOwned)
		assert.Equal(t, "Globex", got[1].CompetitorName)
	})

	t.Run("duplicate urls are collapsed", func(t *testing.T) {
		got := ExtractCitations(
			"https://acme.com/a and again https://acme.com/a",
			[]string{"https://acme.com/a"},
			profile,
		)
		require.Len(t, got, 1)
	})

	t.Run("markdown parentheses do not leak into the url", func(t *testing.T) {
		got := ExtractCitations("[Acme](https://acme.com/pricing)", nil, profile)
		require.Len(t, got, 1)
		assert.Equal(t, "https://acme.com/pricing", got[0].URL)
	})

	t.Run("non-http urls and bare hosts are ignored", func(t *testing.T) {
		got := ExtractCitations("Visit acme.com or ftp://acme.com/file", nil, profile)
		assert.Empty(t, got)
	})

	t.Run("no urls yields an empty slice, not nil", func(t *testing.T) {
		got := ExtractCitations("No links here.", nil, profile)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("nil profile classifies nothing as owned", func(t *testing.T) {
		got := ExtractCitations("https://acme.com/x", nil, nil)
		require.Len(t, got, 1)
		assert.False(t, got[0].IsOwned)
		assert.Empty(t, got[0].CompetitorName)
	})

	t.Run("owned domains given as full urls still match", func(t *testing.T) {
		p := &models.AEOProfile{OwnedDomains: []string{"https://www.acme.com/"}}
		got := ExtractCitations("https://acme.com/x", nil, p)
		require.Len(t, got, 1)
		assert.True(t, got[0].IsOwned)
	})
}
