package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// Profile is one fictional business the generator can build. The engine in
// generate.go is generic: every name, list and volume of a business lives in
// its profile, so a second business is a second profile file and a registry
// entry, not a second engine.
type Profile struct {
	// Name is the --profile value.
	Name string
	// BusinessName is written to general.company_name.
	BusinessName string
	// StaffDomain is the email domain of every staff account; it must be a
	// reserved name (*.example).
	StaffDomain string
	// DemoPassword is the one password every generated account shares. It is
	// a local-demo value, printed once in the summary.
	DemoPassword string
	// Staff are the staff accounts in creation order. The first is the
	// bootstrap admin and must have the admin role.
	Staff []StaffMember
	// CustomerLogin is the one customer account; it is linked to a customer
	// record that receives tickets.
	CustomerLogin StaffMember
	// CustomRole is the role the Custom staff members get.
	CustomRole RoleDef

	Currency           string
	DealAmountMinCents int64
	DealAmountMaxCents int64

	Products []ProductDef
	Labels   []LabelDef

	// CompanyStems x CompanyTrades are the fictional company names; the
	// first Volumes.Companies of them (in a seeded order) become company
	// records and the rest are free-text employers of unlinked leads.
	CompanyStems  []string
	CompanyTrades []string
	Industries    []string
	Countries     []string

	LeadSources []string
	Positions   []string

	Platforms   []string
	DealWorks   []string
	LostReasons []string

	TicketSubjects []string
	CommentOpeners []string
	Resolutions    []string

	TaskTitles []string

	NotificationTitles []string

	Form     models.Form
	Webhooks []WebhookDef
	// Rules returns the automation rules; exactly one may be enabled.
	Rules func() []models.AutomationRule

	Volumes Volumes
}

// StaffMember is one generated account.
type StaffMember struct {
	First, Last string
	Role        models.UserRole
	// Email overrides the derived first.last@StaffDomain address.
	Email string
}

// RoleDef is a custom role created through the role service.
type RoleDef struct {
	Name        string
	Description string
	Permissions []service.RolePermissionInput
}

// ProductDef is one catalogue entry.
type ProductDef struct {
	Name, SKU, Description string
	PriceCents             int64
}

// LabelDef is one task label.
type LabelDef struct {
	Name, Color string
}

// WebhookDef is one outbound webhook endpoint, always created inactive.
type WebhookDef struct {
	URL        string
	EventTypes []string
}

// Volumes are the record counts at scale 1. Every field is scaled by
// --scale; the fixed sets (users, products, labels, rules, the form and the
// webhooks) are the lengths of the profile's lists instead.
type Volumes struct {
	Leads          int
	ConvertedLeads int
	Customers      int
	Companies      int
	Deals          int
	Quotes         int
	Tickets        int
	Tasks          int
	Notifications  int
}

// scaledCount is max(1, round(n*scale)).
func scaledCount(n int, scale float64) int {
	v := int(math.Round(float64(n) * scale))
	if v < 1 {
		return 1
	}
	return v
}

// Scaled returns the volumes multiplied by scale.
func (v Volumes) Scaled(scale float64) Volumes {
	return Volumes{
		Leads:          scaledCount(v.Leads, scale),
		ConvertedLeads: scaledCount(v.ConvertedLeads, scale),
		Customers:      scaledCount(v.Customers, scale),
		Companies:      scaledCount(v.Companies, scale),
		Deals:          scaledCount(v.Deals, scale),
		Quotes:         scaledCount(v.Quotes, scale),
		Tickets:        scaledCount(v.Tickets, scale),
		Tasks:          scaledCount(v.Tasks, scale),
		Notifications:  scaledCount(v.Notifications, scale),
	}
}

// AdminEmail is the bootstrap admin's sign-in address.
func (p *Profile) AdminEmail() string {
	return p.staffEmail(p.Staff[0])
}

func (p *Profile) staffEmail(m StaffMember) string {
	if m.Email != "" {
		return m.Email
	}
	return slug(m.First, ".") + "." + slug(m.Last, ".") + "@" + p.StaffDomain
}

// profiles is the registry of --profile values.
var profiles = map[string]func() *Profile{
	"halvenmark": halvenmarkProfile,
}

func profileNames() []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func lookupProfile(name string) (*Profile, error) {
	build, ok := profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q: valid profiles are %s", name, strings.Join(profileNames(), ", "))
	}
	return build(), nil
}

// slug lower-cases s and keeps only ASCII letters and digits, joining the
// runs between them with sep.
func slug(s, sep string) string {
	var b strings.Builder
	pending := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pending && b.Len() > 0 {
				b.WriteString(sep)
			}
			pending = false
			b.WriteRune(r)
			continue
		}
		pending = true
	}
	return b.String()
}
