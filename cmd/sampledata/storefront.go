package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/florinel-chis/gophercrm/internal/forms"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// Storefront profiles.
//
// A storefront profile is an online shop whose records arrive the way a live
// shop's do: shoppers write through the "Contact us" form (double opt-in,
// ticket intake), boutiques through a lead form and readers through a
// newsletter form, all through FormService.SubmitPublic and, for the contact
// form, FormService.ConfirmSubmission. The ticket a confirmation opens is
// then handled by staff through the ticket service, and the automation
// events the form path records are dispatched and executed in-process by the
// automation engine, as the server's worker would.
//
// None of those paths takes a clock, so every run-time stamp they write
// (submission, token, mail delivery, ticket, key, event, run, run step, and
// the task and notification a step creates) is moved into the history
// afterwards by the backdate plan. restamp writes only the columns the real
// path left non-NULL.

// Storefront holds what a storefront profile needs beyond Profile.
type Storefront struct {
	// Origin is the shop's public origin; submissions are sent from it.
	Origin string
	// SpamOrigin is an origin the forms do not allow.
	SpamOrigin string
	// ShopperDomains are the reserved domains of shopper addresses.
	ShopperDomains []string
	// Pages are the paths submissions are sent from, per form.
	ContactPages, WholesalePages, NewsletterPages []string
	// UserAgents are browsers; BotAgents send the spam. Neither holds a URL.
	UserAgents, BotAgents []string
	// Messages are contact-form messages by topic.
	Messages          map[string][]string
	SpamMessages      []string
	WholesaleMessages []string
}

func (g *generator) storefrontSteps() []genStep {
	return []genStep{
		{"users", g.genUsers},
		{"settings", g.genSettings},
		{"labels", g.genLabels},
		{"products", g.genProducts},
		{"forms", g.genForms},
		// Before the first submission, so the outbox events the
		// submissions record find their rules.
		{"automation rules", g.genRules},
		{"companies", g.genCompanies},
		{"shoppers", g.genShoppers},
		{"contact submissions", g.genContactSubmissions},
		{"wholesale enquiries", g.genWholesale},
		{"deals and quotes", g.genDealsAndQuotes},
		{"newsletter sign-ups", g.genNewsletter},
		{"tasks", g.genTasks},
		{"automation outbox", g.drainAutomation},
		{"automation history", g.planAutomationHistory},
		{"backdating", func() error { return g.plan.apply(g.db) }},
	}
}

// spamPlannedLabel starts the summary line that reports the planned spam.
const spamPlannedLabel = "contact form spam planned:"

// summaryLines are the profile-specific lines of the summary; none for
// halvenmark.
func (g *generator) summaryLines() []string {
	if g.p.Storefront == nil {
		return nil
	}
	return []string{fmt.Sprintf("%s %d", spamPlannedLabel, g.spamPlanned)}
}

// ---- capturing mailer --------------------------------------------------------

// captureMailer is the storefront form service's mailer. It keeps the last
// body per recipient in memory, so the generator can read the confirmation
// link that exists only there. It opens no connection, logs nothing and
// reports success, so every delivery row ends sent and nothing is retried on
// boot.
type captureMailer struct {
	mu   sync.Mutex
	last map[string]string
}

func newCaptureMailer() *captureMailer {
	return &captureMailer{last: map[string]string{}}
}

func (m *captureMailer) Send(to, _, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last[strings.ToLower(strings.TrimSpace(to))] = body
	return nil
}

func (m *captureMailer) SendPasswordReset(string, string, string) error { return nil }

// take returns and forgets the last body sent to address.
func (m *captureMailer) take(address string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(address))
	body, ok := m.last[key]
	delete(m.last, key)
	return body, ok
}

var confirmTokenPattern = regexp.MustCompile(`[?&]token=([^\s&]+)`)

// confirmationToken extracts the raw token from a confirmation mail body.
// It is used once and never printed or stored.
func confirmationToken(body string) (string, error) {
	m := confirmTokenPattern.FindStringSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("the confirmation mail holds no link")
	}
	return url.QueryUnescape(m[1])
}

// formChallengeSecret is the time-trap key, resolved as the form service
// resolves it: API_KEY_SECRET, else JWT_SECRET.
func formChallengeSecret() []byte {
	if secret := os.Getenv("API_KEY_SECRET"); secret != "" {
		return []byte(secret)
	}
	return []byte(os.Getenv("JWT_SECRET"))
}

// ---- time helpers --------------------------------------------------------------

// clamp keeps t inside the window's end.
func (g *generator) clamp(t time.Time) time.Time {
	if t.After(g.anchor) {
		return g.anchor
	}
	return t.Truncate(time.Second)
}

// laterSeconds is t plus a jittered number of seconds, never after the anchor.
func (g *generator) laterSeconds(t time.Time, lo, hi int) time.Time {
	return g.clamp(t.Add(time.Duration(g.fake.IntRange(lo, hi)) * time.Second))
}

// seasonalTimes returns n ascending times over the window. Each calendar
// month gets a share in proportion to its days inside the window times its
// weight (November and December 3, every other month 1), rounded with the
// largest-remainder method; the times are spread with jitter inside the
// month.
func (g *generator) seasonalTimes(n int) []time.Time {
	type slot struct {
		lo, hi time.Time
		weight float64
		count  int
	}
	end := g.anchor.Add(-time.Hour)
	var slots []slot
	total := 0.0
	for lo := g.start; lo.Before(end); {
		next := time.Date(lo.Year(), lo.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		hi := next
		if hi.After(end) {
			hi = end
		}
		w := 1.0
		if lo.Month() == time.November || lo.Month() == time.December {
			w = 3
		}
		w *= hi.Sub(lo).Hours() / 24
		slots = append(slots, slot{lo: lo, hi: hi, weight: w})
		total += w
		lo = hi
	}
	type remainder struct {
		index int
		frac  float64
	}
	assigned := 0
	rems := make([]remainder, len(slots))
	for i := range slots {
		exact := float64(n) * slots[i].weight / total
		slots[i].count = int(exact)
		assigned += slots[i].count
		rems[i] = remainder{i, exact - float64(slots[i].count)}
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; assigned < n; k++ {
		slots[rems[k%len(rems)].index].count++
		assigned++
	}
	times := make([]time.Time, 0, n)
	for _, s := range slots {
		for j := 0; j < s.count; j++ {
			times = append(times, g.spread(j, s.count, s.lo, s.hi))
		}
	}
	return times
}

// testNetIP is an address in one of the RFC 5737 documentation ranges.
func (g *generator) testNetIP() string {
	prefix := pickOne(g, []string{"192.0.2", "198.51.100", "203.0.113"})
	return fmt.Sprintf("%s.%d", prefix, 1+g.intn(254))
}

// ---- backdating helpers ----------------------------------------------------------

// restamp plans cols for one row, keeping only the columns the real path
// left non-NULL: a stamp the service never wrote stays NULL.
func (g *generator) restamp(model any, id uint, cols map[string]any) error {
	names := make([]string, 0, len(cols))
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)
	row := map[string]any{}
	if err := g.db.Model(model).Select(names).Where("id = ?", id).Take(&row).Error; err != nil {
		return fmt.Errorf("reading %T %d: %w", model, id, err)
	}
	for _, name := range names {
		if row[name] == nil {
			delete(cols, name)
		}
	}
	if len(cols) > 0 {
		g.plan.set(model, id, cols)
	}
	return nil
}

// restampWhere is restamp for every row whose column equals value.
func (g *generator) restampWhere(model any, column string, value uint, cols func() map[string]any) error {
	var ids []uint
	if err := g.db.Model(model).Where(column+" = ?", value).Order("id").Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := g.restamp(model, id, cols()); err != nil {
			return err
		}
	}
	return nil
}

// ---- shoppers ------------------------------------------------------------------

// genShoppers writes the shopper customers. The first is the customer
// login's record. Shoppers are spread over the window in id order, so the
// shoppers that exist at a time are a prefix of the list.
func (g *generator) genShoppers() error {
	sf := g.p.Storefront
	n := g.vol.Customers - g.vol.ConvertedLeads
	if n < 1 {
		n = 1
	}
	m := g.p.CustomerLogin
	loginEmail := g.email(m.First, m.Last, sf.ShopperDomains[0])
	user := &models.User{Email: loginEmail, FirstName: m.First, LastName: m.Last, Role: models.RoleCustomer, IsActive: true}
	if err := g.s.users.CreateAs(g.ctx, g.actor, user, g.p.DemoPassword); err != nil {
		return err
	}
	g.customerUser = user.ID
	g.roleByID[user.ID] = models.RoleCustomer
	g.plan.at(&models.User{}, user.ID, g.start.Add(time.Duration(len(g.p.Staff))*time.Hour))

	for i := 0; i < n; i++ {
		first, last, email := m.First, m.Last, loginEmail
		if i > 0 {
			first, last = g.fake.FirstName(), g.fake.LastName()
			email = g.email(first, last, sf.ShopperDomains[i%len(sf.ShopperDomains)])
		}
		customer := &models.Customer{
			FirstName:  first,
			LastName:   last,
			Email:      email,
			Phone:      g.phone(),
			Address:    g.fake.Street(),
			City:       g.fake.City(),
			Country:    pickOne(g, g.p.Countries),
			PostalCode: g.fake.Zip(),
			Notes:      "Online shopper.",
		}
		if i == 0 {
			customer.UserID = ptr(g.customerUser)
		}
		if err := g.s.customers.Create(g.ctx, customer); err != nil {
			return err
		}
		created := g.history(i, n)
		if i == 0 {
			created = g.start.Add(36 * time.Hour)
		}
		g.plan.at(&models.Customer{}, customer.ID, created)
		rec := genCustomer{id: customer.ID, created: created, email: email, first: first, last: last}
		if i == 0 {
			g.login = rec
		}
		g.shoppers = append(g.shoppers, rec)
	}
	return nil
}

// shopperBefore picks a shopper whose record exists at t, else the login's.
func (g *generator) shopperBefore(t time.Time) genCustomer {
	rest := g.shoppers[1:]
	k := sort.Search(len(rest), func(j int) bool { return !rest[j].created.Before(t) })
	if k == 0 {
		return g.shoppers[0]
	}
	return rest[g.intn(k)]
}

// ---- submissions -----------------------------------------------------------------

type contactKind int

const (
	contactShopper contactKind = iota
	contactUnknown
	contactHoneypot
	contactDomain
	contactTimeTrap
)

func (k contactKind) spam() bool { return k >= contactHoneypot }

// submit sends one submission through the public path and returns the id
// of the row it stored.
func (g *generator) submit(formName string, values map[string]string, origin, ua, page string, challenge, honeypot string) (uint, error) {
	form := g.forms[formName]
	req := &service.PublicSubmissionRequest{Values: values, Challenge: challenge, Honeypot: honeypot, PageURL: page}
	meta := service.SubmissionMeta{IP: g.testNetIP(), UserAgent: ua, Origin: origin}
	if _, err := g.s.forms.SubmitPublic(g.ctx, form.PublicID, req, meta); err != nil {
		return 0, fmt.Errorf("%s submission: %w", formName, err)
	}
	var ids []uint
	if err := g.db.Model(&models.FormSubmission{}).Where("form_id = ?", form.ID).Order("id DESC").Limit(1).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) != 1 {
		return 0, fmt.Errorf("%s submission was not stored", formName)
	}
	return ids[0], nil
}

// genuineChallenge is a challenge issued ten minutes ago, as for a visitor
// who took that long to fill in the form.
func genuineChallenge(secret []byte) string {
	return forms.NewChallenge(secret, time.Now().Add(-10*time.Minute))
}

func (g *generator) genContactSubmissions() error {
	sf := g.p.Storefront
	secret := formChallengeSecret()
	n := g.vol.ContactSubmissions
	times := g.seasonalTimes(n)
	opening := []contactKind{contactHoneypot, contactDomain, contactTimeTrap, contactUnknown}
	spamKinds := []contactKind{contactHoneypot, contactDomain, contactTimeTrap}
	genuine, handled := 0, 0
	for i, t := range times {
		var kind contactKind
		switch r := g.intn(100); {
		case i < len(opening):
			kind = opening[i]
		case r < 92:
			kind = contactShopper
		case r < 96:
			kind = contactUnknown
		default:
			kind = pickOne(g, spamKinds)
		}

		topic := cycleOrWeighted(g, i, pelicroftContactTopics, []int{35, 25, 15, 15, 10})
		var first, last, email string
		switch kind {
		case contactShopper:
			shopper := g.shopperBefore(t)
			if genuine%15 == 0 && t.After(g.login.created) {
				shopper = g.login
			}
			genuine++
			first, last, email = shopper.first, shopper.last, shopper.email
		case contactUnknown:
			first, last = g.fake.FirstName(), g.fake.LastName()
			email = g.email(first, last, "example.com")
		default:
			first, last = g.fake.FirstName(), g.fake.LastName()
			email = g.email(first, last, "example.net")
		}
		message := pickOne(g, sf.Messages[topic])
		orderNumber := ""
		if topic != "Product question" && topic != "Other" || g.chance(30) {
			orderNumber = fmt.Sprintf("PH-%06d", 100000+g.intn(900000))
		}
		ua, origin, honeypot := pickOne(g, sf.UserAgents), sf.Origin, ""
		challenge := genuineChallenge(secret)
		switch kind {
		case contactHoneypot:
			ua, honeypot, message = pickOne(g, sf.BotAgents), "yes", pickOne(g, sf.SpamMessages)
		case contactDomain:
			ua, origin, message = pickOne(g, sf.BotAgents), sf.SpamOrigin, pickOne(g, sf.SpamMessages)
		case contactTimeTrap:
			// Issued in the future: a negative age, which the time trap
			// files as spam whatever the clock says.
			ua, message = pickOne(g, sf.BotAgents), pickOne(g, sf.SpamMessages)
			challenge = forms.NewChallenge(secret, time.Now().Add(time.Hour))
		}
		values := map[string]string{
			"name": first + " " + last, "email": email, "order_number": orderNumber,
			"topic": topic, "message": message,
		}
		subID, err := g.submit(pelicroftContactForm, values, origin, ua, sf.Origin+pickOne(g, sf.ContactPages), challenge, honeypot)
		if err != nil {
			return err
		}
		if kind.spam() {
			g.spamPlanned++
			if err := g.restamp(&models.FormSubmission{}, subID, map[string]any{"created_at": t, "updated_at": t}); err != nil {
				return err
			}
			continue
		}

		// Confirmed at once, from the link in the captured mail: no
		// submission and no token stays pending.
		body, ok := g.s.capture.take(email)
		if !ok {
			return fmt.Errorf("contact submission %d: no confirmation mail was captured", subID)
		}
		token, err := confirmationToken(body)
		if err != nil {
			return fmt.Errorf("contact submission %d: %w", subID, err)
		}
		if err := g.s.forms.ConfirmSubmission(g.ctx, token); err != nil {
			return fmt.Errorf("confirming contact submission %d: %w", subID, err)
		}
		var stored models.FormSubmission
		if err := g.db.Select("id", "ticket_id").First(&stored, subID).Error; err != nil {
			return err
		}

		mailed := g.laterSeconds(t, 5, 60)
		confirmed := g.laterSeconds(t, 3*60, 6*60*60)
		if err := g.restamp(&models.FormSubmission{}, subID, map[string]any{
			"created_at": t, "updated_at": confirmed, "confirmed_at": confirmed,
		}); err != nil {
			return err
		}
		if err := g.restampWhere(&models.FormConfirmationToken{}, "submission_id", subID, func() map[string]any {
			return map[string]any{"created_at": t, "updated_at": confirmed, "used_at": confirmed, "expires_at": t.Add(48 * time.Hour)}
		}); err != nil {
			return err
		}
		if err := g.restampWhere(&models.FormMailDelivery{}, "submission_id", subID, func() map[string]any {
			return map[string]any{"created_at": t, "updated_at": mailed, "last_attempt_at": mailed, "sent_at": mailed}
		}); err != nil {
			return err
		}
		if stored.TicketID == nil {
			continue
		}
		if err := g.restampWhere(&models.FormTicketKey{}, "submission_id", subID, func() map[string]any {
			return map[string]any{"created_at": confirmed, "updated_at": confirmed}
		}); err != nil {
			return err
		}
		if err := g.handleTicket(*stored.TicketID, confirmed, topic, handled); err != nil {
			return fmt.Errorf("ticket %d: %w", *stored.TicketID, err)
		}
		handled++
	}
	return nil
}

// topicPriority is the priority support gives a contact-form ticket once it
// has read the submission.
var topicPriority = map[string]models.TicketPriority{
	"Order status":       models.TicketPriorityMedium,
	"Return or exchange": models.TicketPriorityMedium,
	"Damaged item":       models.TicketPriorityHigh,
	"Product question":   models.TicketPriorityLow,
	"Other":              models.TicketPriorityLow,
}

// handleTicket is the staff side of a ticket the intake opened: comments
// by support or admin, then an assignee, a priority, a due date, a status
// and, when resolved or closed, a resolution.
func (g *generator) handleTicket(id uint, created time.Time, topic string, k int) error {
	statuses := []models.TicketStatus{models.TicketStatusOpen, models.TicketStatusInProgress, models.TicketStatusResolved, models.TicketStatusClosed}
	status := cycleOrWeighted(g, k, statuses, []int{15, 20, 40, 25})
	assignee := pickOne(g, g.support)
	comments := 1 + g.intn(6)
	at := created
	var firstResponse *time.Time
	for c := 0; c < comments; c++ {
		author := assignee
		switch r := g.intn(100); {
		case r < 15:
			author = g.admin
		case r < 30:
			author = pickOne(g, g.support)
		}
		internal := g.chance(25)
		if c == 0 {
			internal = g.chance(10)
		}
		comment := &models.TicketComment{
			TicketID:     id,
			AuthorUserID: author,
			Body:         pickOne(g, g.p.CommentOpeners) + " " + g.fake.Sentence(),
			IsInternal:   internal,
		}
		// CreateComment writes no ticket_mail_deliveries row: the customer
		// is never mailed.
		if err := g.s.tickets.CreateComment(comment); err != nil {
			return err
		}
		at = g.later(at, 1, 48)
		g.plan.at(&models.TicketComment{}, comment.ID, at)
		if firstResponse == nil && !internal {
			firstResponse = ptr(at)
		}
	}

	ticket, err := g.s.tickets.GetByID(id)
	if err != nil {
		return err
	}
	// Only the ticket's own columns are written back.
	ticket.Customer = models.Customer{}
	ticket.AssignedTo = nil
	ticket.Comments = nil
	ticket.AssignedToID = ptr(assignee)
	ticket.Priority = topicPriority[topic]
	ticket.Status = status
	ticket.DueAt = ptr(time.Now().UTC().Add(72 * time.Hour))
	if status == models.TicketStatusResolved || status == models.TicketStatusClosed {
		ticket.Resolution = pickOne(g, g.p.Resolutions)
	}
	if err := g.s.tickets.Update(ticket); err != nil {
		return err
	}
	cols := map[string]any{"created_at": created, "updated_at": at, "due_at": created.Add(72 * time.Hour)}
	if firstResponse != nil {
		cols["first_responded_at"] = *firstResponse
	} else {
		cols["first_responded_at"] = at
	}
	g.ticketTimes[id] = created
	return g.restamp(&models.Ticket{}, id, cols)
}

func (g *generator) genWholesale() error {
	sf := g.p.Storefront
	secret := formChallengeSecret()
	n := g.vol.WholesaleEnquiries
	converted := g.vol.ConvertedLeads
	if converted > n {
		converted = n
	}
	convert := convertedIndexes(n, converted)
	open := []models.LeadStatus{models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusQualified}
	k := 0
	for i := 0; i < n; i++ {
		t := g.history(i, n)
		first, last := g.fake.FirstName(), g.fake.LastName()
		var company *genCompany
		var employer, domain string
		if convert[i] {
			company = &g.companies[k%len(g.companies)]
			k++
			employer, domain = company.name, company.domain
		} else {
			employer = g.companyName(len(g.companies) + i)
			domain = slug(employer, "-") + ".example"
		}
		email := g.email(first, last, domain)
		values := map[string]string{
			"name": first + " " + last, "email": email, "company": employer,
			"phone": g.phone(), "message": pickOne(g, sf.WholesaleMessages),
		}
		subID, err := g.submit(pelicroftWholesaleForm, values, sf.Origin, pickOne(g, sf.UserAgents),
			sf.Origin+pickOne(g, sf.WholesalePages), genuineChallenge(secret), "")
		if err != nil {
			return err
		}
		var stored models.FormSubmission
		if err := g.db.Select("id", "lead_id").First(&stored, subID).Error; err != nil {
			return err
		}
		if stored.LeadID == nil {
			return fmt.Errorf("wholesale submission %d created no lead", subID)
		}
		leadID := *stored.LeadID
		if err := g.restamp(&models.FormSubmission{}, subID, map[string]any{"created_at": t, "updated_at": t}); err != nil {
			return err
		}
		lead, err := g.s.leads.GetByID(leadID)
		if err != nil {
			return err
		}
		rec := genLead{id: leadID, owner: lead.OwnerID, created: t, status: models.LeadStatusNew}

		switch {
		case convert[i]:
			if _, err := g.s.leads.Update(leadID, map[string]interface{}{
				"status": models.LeadStatusQualified, "company_id": ptr(company.id),
			}); err != nil {
				return fmt.Errorf("qualifying lead %d: %w", leadID, err)
			}
			customer, err := g.s.leads.ConvertToCustomer(g.ctx, leadID, &models.Customer{
				Website:    "https://www." + domain,
				Address:    g.fake.Street(),
				City:       g.fake.City(),
				Country:    pickOne(g, g.p.Countries),
				PostalCode: g.fake.Zip(),
				Notes:      "Wholesale account opened from an enquiry.",
			})
			if err != nil {
				return fmt.Errorf("converting lead %d: %w", leadID, err)
			}
			convertedAt := g.later(t, 48, 30*24)
			g.plan.stamp(&models.Lead{}, leadID, t, convertedAt)
			g.plan.at(&models.Customer{}, customer.ID, convertedAt)
			g.customers = append(g.customers, genCustomer{id: customer.ID, companyID: customer.CompanyID, created: convertedAt})
			rec.status = models.LeadStatusConverted
		default:
			status := cycleOrWeighted(g, i, open, []int{40, 35, 25})
			updated := t
			if status != models.LeadStatusNew {
				if _, err := g.s.leads.Update(leadID, map[string]interface{}{"status": status}); err != nil {
					return fmt.Errorf("moving lead %d to %s: %w", leadID, status, err)
				}
				updated = g.later(t, 2, 10*24)
			}
			g.plan.stamp(&models.Lead{}, leadID, t, updated)
			rec.status = status
		}
		g.leadTimes[leadID] = t
		g.leads = append(g.leads, rec)
	}
	if len(g.customers) == 0 {
		return fmt.Errorf("profile %s converted no wholesale lead", g.p.Name)
	}
	return nil
}

func (g *generator) genNewsletter() error {
	sf := g.p.Storefront
	secret := formChallengeSecret()
	n := g.vol.NewsletterSignups
	for i := 0; i < n; i++ {
		t := g.history(i, n)
		first, last := g.fake.FirstName(), g.fake.LastName()
		values := map[string]string{"email": g.email(first, last, sf.ShopperDomains[i%len(sf.ShopperDomains)])}
		if g.chance(70) {
			values["first_name"] = first
		}
		subID, err := g.submit(pelicroftNewsletterForm, values, sf.Origin, pickOne(g, sf.UserAgents),
			sf.Origin+pickOne(g, sf.NewsletterPages), genuineChallenge(secret), "")
		if err != nil {
			return err
		}
		if err := g.restamp(&models.FormSubmission{}, subID, map[string]any{"created_at": t, "updated_at": t}); err != nil {
			return err
		}
	}
	return nil
}

// ---- automation -------------------------------------------------------------------

// drainAutomation dispatches and executes the outbox the form path filled,
// with the engine the server's worker uses, until no event is unprocessed
// and no run is unfinished. Every run must have completed.
func (g *generator) drainAutomation() error {
	final := []string{models.AutomationRunCompleted, models.AutomationRunFailed, models.AutomationRunCancelled}
	for guard := 0; ; guard++ {
		if guard > 100000 {
			return fmt.Errorf("the automation outbox did not drain")
		}
		if _, err := g.s.engine.Dispatch(g.ctx); err != nil {
			return err
		}
		if err := g.s.engine.ExecuteDue(g.ctx); err != nil {
			return err
		}
		var events, runs int64
		if err := g.db.Model(&models.AutomationEvent{}).Where("processed_at IS NULL").Count(&events).Error; err != nil {
			return err
		}
		if err := g.db.Model(&models.AutomationRun{}).Where("status NOT IN ?", final).Count(&runs).Error; err != nil {
			return err
		}
		if events == 0 && runs == 0 {
			break
		}
	}
	var unfinished int64
	if err := g.db.Model(&models.AutomationRun{}).Where("status <> ?", models.AutomationRunCompleted).Count(&unfinished).Error; err != nil {
		return err
	}
	if unfinished > 0 {
		return fmt.Errorf("%d automation runs did not complete", unfinished)
	}
	return nil
}

// planAutomationHistory moves the drained outbox into the history: an event
// at the time of the record it belongs to, processed seconds later; a run
// created when its event was processed, claimed, executed and finished in
// that order; the task or notification a step made at the step's time, a
// task due the rule's days after it.
func (g *generator) planAutomationHistory() error {
	var events []models.AutomationEvent
	if err := g.db.Order("id").Find(&events).Error; err != nil {
		return err
	}
	processed := map[uint]time.Time{}
	for _, ev := range events {
		var base time.Time
		var ok bool
		switch ev.EntityType {
		case models.AutomationEntityTicket:
			base, ok = g.ticketTimes[ev.EntityID]
		case models.AutomationEntityLead:
			base, ok = g.leadTimes[ev.EntityID]
		}
		if !ok {
			return fmt.Errorf("automation event %d (%s %d) has no generated time", ev.ID, ev.EntityType, ev.EntityID)
		}
		at := g.laterSeconds(base, 5, 120)
		processed[ev.ID] = at
		if err := g.restamp(&models.AutomationEvent{}, ev.ID, map[string]any{"created_at": base, "processed_at": at}); err != nil {
			return err
		}
	}

	var steps []models.AutomationRunStep
	if err := g.db.Order("id").Find(&steps).Error; err != nil {
		return err
	}
	stepsOf := map[uint][]models.AutomationRunStep{}
	for _, st := range steps {
		stepsOf[st.RunID] = append(stepsOf[st.RunID], st)
	}

	var runs []models.AutomationRun
	if err := g.db.Order("id").Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		if run.EventID == nil {
			return fmt.Errorf("automation run %d has no event", run.ID)
		}
		created, ok := processed[*run.EventID]
		if !ok {
			return fmt.Errorf("automation run %d names an unknown event", run.ID)
		}
		claimed := g.laterSeconds(created, 1, 60)
		at := claimed
		for _, st := range stepsOf[run.ID] {
			at = g.laterSeconds(at, 1, 3)
			if err := g.planStepEffects(st, at); err != nil {
				return err
			}
		}
		finished := g.laterSeconds(at, 1, 3)
		if err := g.restamp(&models.AutomationRun{}, run.ID, map[string]any{
			"created_at": created, "updated_at": finished, "resume_at": created,
			"claimed_at": claimed, "heartbeat_at": claimed, "finished_at": finished,
		}); err != nil {
			return err
		}
	}
	return nil
}

// planStepEffects stamps one run step at at, with the task or notification
// it created.
func (g *generator) planStepEffects(st models.AutomationRunStep, at time.Time) error {
	cols := map[string]any{"executed_at": at}
	if id, ok := detailID(st.Details, "task_id"); ok {
		var task models.Task
		if err := g.db.Select("id", "due_date").First(&task, id).Error; err != nil {
			return err
		}
		if task.DueDate == nil {
			return fmt.Errorf("automation task %d has no due date", id)
		}
		// The rule's due_in_days, as the step applied it to its run time.
		delta := task.DueDate.Sub(st.ExecutedAt).Round(time.Hour)
		due := at.Add(delta).UTC()
		g.plan.set(&models.Task{}, id, map[string]any{"created_at": at, "updated_at": at, "due_date": due})
		details := map[string]any{}
		for k, v := range st.Details {
			details[k] = v
		}
		details["due_date"] = due.Format(time.RFC3339)
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		cols["detail"] = string(encoded)
	}
	if id, ok := detailID(st.Details, "notification_id"); ok {
		if err := g.restamp(&models.Notification{}, id, map[string]any{"created_at": at, "updated_at": at, "read_at": at}); err != nil {
			return err
		}
	}
	g.plan.set(&models.AutomationRunStep{}, st.ID, cols)
	return nil
}

// detailID reads a numeric id from a run step's decoded details.
func detailID(details map[string]interface{}, key string) (uint, bool) {
	switch v := details[key].(type) {
	case float64:
		return uint(v), v > 0
	case json.Number:
		n, err := v.Int64()
		return uint(n), err == nil && n > 0
	}
	return 0, false
}
