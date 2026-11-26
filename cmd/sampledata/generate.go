package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/florinel-chis/gophercrm/internal/config"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/service"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"gorm.io/gorm"
)

// backdateMonths is how far back the generated history reaches.
const backdateMonths = 18

// services are the shipped services the generator writes through, built
// with the constructors and options setupDependencies in cmd/main.go uses.
//
// halvenmark: every service is left without the automation-event and
// webhook-emitter options (a fresh database must hold no pending automation
// event and no webhook delivery, or the server's workers would act on first
// boot), and every mail goes to a log-only mailer.
//
// A storefront profile (pelicroft) uses the same services with one
// difference: its form service also gets WithFormAutomationEvents and a
// capturing mailer (captureMailer, storefront.go), because its tickets and
// leads arrive through the public submission path and the confirmation link
// exists only in the mail body. The events that path records are drained
// in-process with the automation engine before the generator finishes, so
// the database still holds no unprocessed event and no unfinished run.
// Neither profile wires a webhook emitter.
//
// Left out for both: the AEO runtime and the quote PDF renderer.
type services struct {
	bootstrap     service.UserService
	users         service.UserService
	roles         service.RoleService
	leads         service.LeadService
	customers     service.CustomerService
	companies     service.CompanyService
	deals         service.DealService
	products      service.ProductService
	quotes        service.QuoteService
	tickets       service.TicketService
	tasks         service.TaskService
	labels        service.LabelService
	forms         service.FormService
	automation    service.AutomationService
	config        service.ConfigurationService
	webhooks      webhook.WebhookService
	notifications repository.NotificationRepository
	// engine is the automation engine the automation service validates and
	// dry-runs with; a storefront profile drains the outbox with it.
	engine *service.AutomationEngine
	// capture holds the confirmation mail of the storefront form service;
	// nil for halvenmark.
	capture *captureMailer
}

func buildServices(db *gorm.DB, cfg *config.Config, p *Profile) *services {
	userRepo := repository.NewUserRepository(db)
	leadRepo := repository.NewLeadRepository(db)
	customerRepo := repository.NewCustomerRepository(db)
	ticketRepo := repository.NewTicketRepository(db)
	taskRepo := repository.NewTaskRepository(db)
	labelRepo := repository.NewLabelRepository(db)
	companyRepo := repository.NewCompanyRepository(db)
	dealRepo := repository.NewDealRepository(db)
	productRepo := repository.NewProductRepository(db)
	quoteRepo := repository.NewQuoteRepository(db)
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	configRepo := repository.NewConfigurationRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)
	formRepo := repository.NewFormRepository(db)
	automationRepo := repository.NewAutomationRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	webhookRepo := repository.NewWebhookRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	roleAuditRepo := repository.NewRoleAuditRepository(db)

	// Log-only, whatever SMTP_* the environment holds: nothing generated is
	// ever mailed.
	appMailer := mailer.NewFromConfig(config.SMTPConfig{})
	txManager := utils.NewTransactionManager(db)

	configService := service.NewConfigurationService(configRepo,
		utils.NewSecretBox(cfg.API.APIKeySecret, "configuration-secret"))

	quoteTokenSecret := cfg.API.APIKeySecret
	if quoteTokenSecret == "" {
		quoteTokenSecret = cfg.JWT.Secret
	}
	automationEngine := service.NewAutomationEngine(automationRepo, notificationRepo, taskRepo, userRepo, appMailer, txManager)

	formOptions := []service.FormServiceOption{
		service.WithFormTicketIntake(repository.NewFormTicketRepository(db), txManager),
	}
	var formMailer mailer.Mailer = appMailer
	var capture *captureMailer
	if p.Storefront != nil {
		capture = newCaptureMailer()
		formMailer = capture
		formOptions = append(formOptions, service.WithFormAutomationEvents(automationRepo))
	}

	return &services{
		// The first admin is registered the way cmd/create-admin does it.
		bootstrap: service.NewUserService(userRepo),
		users: service.NewUserService(userRepo,
			service.WithUserRoleGovernance(roleRepo, roleAuditRepo, txManager),
			service.WithUserCredentialRevocation(apiKeyRepo, refreshTokenRepo)),
		roles: service.NewRoleService(roleRepo, roleAuditRepo, userRepo, txManager),
		leads: service.NewLeadService(leadRepo, customerRepo, companyRepo, userRepo, txManager,
			service.WithLeadConversionRules(configService)),
		customers: service.NewCustomerService(customerRepo, userRepo, companyRepo, txManager),
		companies: service.NewCompanyService(companyRepo, userRepo, txManager),
		deals:     service.NewDealService(dealRepo, companyRepo, customerRepo, leadRepo, userRepo, configService, txManager),
		products: service.NewProductService(productRepo, configService,
			service.WithProductExternalIDs(repository.NewProductExternalIDRepository(db), txManager)),
		quotes: service.NewQuoteServiceWithResponseLinks(quoteRepo, productRepo, dealRepo, customerRepo, userRepo, configService, appMailer, txManager,
			repository.NewQuoteResponseTokenRepository(db), cfg.App.BaseURL, quoteTokenSecret),
		tickets: service.NewTicketService(ticketRepo, customerRepo, userRepo,
			service.WithTicketCommentMail(repository.NewTicketMailDeliveryRepository(db), txManager, appMailer),
			service.WithTicketProductName(configService)),
		tasks: service.NewTaskService(taskRepo, userRepo, leadRepo, customerRepo, labelRepo, dealRepo,
			service.WithTaskRowLock(txManager)),
		labels: service.NewLabelService(labelRepo),
		forms: service.NewFormService(formRepo, leadRepo, userRepo, formMailer,
			txManager, cfg.Forms, cfg.API.Prefix, formOptions...),
		automation: service.NewAutomationService(automationRepo, userRepo, automationEngine, txManager),
		config:     configService,
		// Endpoint secrets are sealed under the same SecretBox context the
		// server reads them with, keyed by API_KEY_SECRET, else JWT_SECRET.
		webhooks:      webhook.NewWebhookService(webhookRepo, txManager, utils.NewSecretBox(cfg.API.APIKeySecret, webhook.WebhookSecretContext)),
		notifications: notificationRepo,
		engine:        automationEngine,
		capture:       capture,
	}
}

type genLead struct {
	id      uint
	owner   uint
	created time.Time
	status  models.LeadStatus
}

type genCompany struct {
	id     uint
	name   string
	domain string
}

type genCustomer struct {
	id        uint
	companyID *uint
	created   time.Time
	// Set for storefront shoppers, who submit the contact form.
	email, first, last string
}

type genDeal struct {
	id       uint
	owner    uint
	customer uint
	target   models.DealStage
	created  time.Time
}

type genTicket struct {
	id uint
}

// generator holds one run's state. All randomness comes from the two seeded
// sources: rnd for structural choices (statuses, links, amounts) and fake
// (gofakeit) for person names, street addresses, sentences, paragraphs and
// date jitter. No other source of randomness may feed a stored value that
// the determinism test hashes.
type generator struct {
	p      *Profile
	vol    Volumes
	s      *services
	db     *gorm.DB
	fake   *gofakeit.Faker
	rnd    *rand.Rand
	ctx    context.Context
	anchor time.Time
	start  time.Time

	admin        uint
	actor        service.Actor
	sales        []uint
	support      []uint
	staffIDs     []uint
	roleByID     map[uint]models.UserRole
	customerUser uint
	// taskStaff are the users manual tasks go to: every staff user for
	// halvenmark, every staff user but the custom-role ones for a storefront
	// profile (only its triage rule gives them tasks).
	taskStaff []uint
	userIDs   map[string]uint
	formIDs   map[string]uint
	forms     map[string]*models.Form

	labelIDs   []uint
	productIDs []uint
	companies  []genCompany
	leads      []genLead
	customers  []genCustomer
	login      genCustomer
	deals      []genDeal
	tickets    []genTicket
	emails     map[string]bool

	// Storefront state (storefront.go).
	shoppers    []genCustomer
	ticketTimes map[uint]time.Time
	leadTimes   map[uint]time.Time
	// spamPlanned counts the contact submissions planned as spam.
	spamPlanned int

	plan backdatePlan
}

func newGenerator(db *gorm.DB, s *services, p *Profile, seed int64, scale float64, anchor time.Time) *generator {
	return &generator{
		p:        p,
		vol:      p.Volumes.Scaled(scale),
		s:        s,
		db:       db,
		fake:     gofakeit.New(uint64(seed)),
		rnd:      rand.New(rand.NewPCG(uint64(seed), 0x68616c76656e)),
		ctx:      context.Background(),
		anchor:   anchor,
		start:    anchor.AddDate(0, -backdateMonths, 0),
		roleByID: map[uint]models.UserRole{},
		emails:   map[string]bool{},
		userIDs:  map[string]uint{},
		formIDs:  map[string]uint{},
		forms:    map[string]*models.Form{},

		ticketTimes: map[uint]time.Time{},
		leadTimes:   map[uint]time.Time{},
	}
}

type genStep struct {
	name string
	fn   func() error
}

func (g *generator) run() error {
	steps := g.halvenmarkSteps()
	if g.p.Storefront != nil {
		steps = g.storefrontSteps()
	}
	for _, step := range steps {
		if err := step.fn(); err != nil {
			return fmt.Errorf("generating %s: %w", step.name, err)
		}
	}
	return nil
}

func (g *generator) halvenmarkSteps() []genStep {
	return []genStep{
		{"users", g.genUsers},
		{"settings", g.genSettings},
		{"labels", g.genLabels},
		{"products", g.genProducts},
		{"companies", g.genCompanies},
		{"leads", g.genLeads},
		{"customers", g.genCustomers},
		{"deals and quotes", g.genDealsAndQuotes},
		{"tickets", g.genTickets},
		{"tasks", g.genTasks},
		{"notifications", g.genNotifications},
		{"automation rules", g.genRules},
		{"form", g.genForms},
		{"webhook endpoints", g.genWebhooks},
		{"backdating", func() error { return g.plan.apply(g.db) }},
	}
}

// ---- randomness helpers ----------------------------------------------------

func (g *generator) intn(n int) int { return g.rnd.IntN(n) }

func (g *generator) chance(percent int) bool { return g.rnd.IntN(100) < percent }

func pickOne[T any](g *generator, values []T) T { return values[g.rnd.IntN(len(values))] }

// cycleOrWeighted returns values[i] for the first len(values) records, so
// every value exists in any set at least that large, and then a weighted
// choice.
func cycleOrWeighted[T any](g *generator, i int, values []T, weights []int) T {
	if i < len(values) {
		return values[i]
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	r := g.rnd.IntN(total)
	for k, w := range weights {
		if r < w {
			return values[k]
		}
		r -= w
	}
	return values[len(values)-1]
}

// spread is the i-th of n times spread evenly over [lo, hi), with gofakeit
// jitter inside each slot.
func (g *generator) spread(i, n int, lo, hi time.Time) time.Time {
	if n < 1 {
		n = 1
	}
	u := g.fake.Float64Range(0, 1)
	offset := time.Duration((float64(i) + u) / float64(n) * float64(hi.Sub(lo)))
	return lo.Add(offset).Truncate(time.Second)
}

// history is a time spread over the whole backdated window.
func (g *generator) history(i, n int) time.Time {
	return g.spread(i, n, g.start, g.anchor.Add(-time.Hour))
}

// later is t plus a jittered number of hours, never after the anchor.
func (g *generator) later(t time.Time, minHours, maxHours int) time.Time {
	next := t.Add(time.Duration(g.fake.IntRange(minHours*60, maxHours*60)) * time.Minute).Truncate(time.Second)
	if next.After(g.anchor) {
		return g.anchor
	}
	return next
}

// phone is a number in a range reserved for drama and examples: Ofcom's
// +44 7700 900xxx or the North American 555-01xx block.
func (g *generator) phone() string {
	if g.chance(50) {
		return fmt.Sprintf("+44 7700 900%03d", g.intn(1000))
	}
	return fmt.Sprintf("+1 %03d 555 01%02d", 201+g.intn(789), g.intn(100))
}

// email returns local@domain, unique across every lead and customer of the
// run (leads and customers both refuse a duplicate).
func (g *generator) email(first, last, domain string) string {
	base := slug(first, ".") + "." + slug(last, ".")
	candidate := base + "@" + domain
	for n := 2; g.emails[candidate]; n++ {
		candidate = fmt.Sprintf("%s%d@%s", base, n, domain)
	}
	g.emails[candidate] = true
	return candidate
}

// companyName is the k-th fictional company name: stems x trades, then the
// same with a qualifier, then numbered, so names stay unique at any scale.
func (g *generator) companyName(k int) string {
	combos := len(g.p.CompanyStems) * len(g.p.CompanyTrades)
	base := k % combos
	name := g.p.CompanyStems[base/len(g.p.CompanyTrades)] + " " + g.p.CompanyTrades[base%len(g.p.CompanyTrades)]
	qualifiers := []string{"", " Collective", " House", " Supply", " Works", " Trading"}
	round := k / combos
	if round == 0 {
		return name
	}
	if round < len(qualifiers) {
		return name + qualifiers[round]
	}
	return fmt.Sprintf("%s %d", name, round)
}

func ptr[T any](v T) *T { return &v }

// ---- users and settings ----------------------------------------------------

func (g *generator) genUsers() error {
	p := g.p
	if len(p.Staff) == 0 || p.Staff[0].Role != models.RoleAdmin {
		return fmt.Errorf("profile %s: the first staff member must be an admin", p.Name)
	}
	// Bootstrap: the first admin, the way cmd/create-admin registers it.
	first := p.Staff[0]
	admin := &models.User{Email: p.staffEmail(first), FirstName: first.First, LastName: first.Last, Role: models.RoleAdmin, IsActive: true}
	if err := g.s.bootstrap.Register(admin, p.DemoPassword); err != nil {
		return err
	}
	g.admin = admin.ID
	g.actor = service.Actor{UserID: admin.ID, AuthMethod: service.AuthMethodBearer}
	g.addStaff(admin.ID, models.RoleAdmin)
	g.userIDs[admin.Email] = admin.ID
	g.plan.at(&models.User{}, admin.ID, g.start)

	var customRoleID *uint
	for i, m := range p.Staff[1:] {
		user := &models.User{Email: p.staffEmail(m), FirstName: m.First, LastName: m.Last, Role: m.Role, IsActive: true}
		if m.Role == models.RoleCustom {
			if customRoleID == nil {
				role, err := g.s.roles.Create(g.ctx, g.actor, service.RoleCreateInput{
					Name: p.CustomRole.Name, Description: p.CustomRole.Description, Permissions: p.CustomRole.Permissions,
				})
				if err != nil {
					return fmt.Errorf("custom role: %w", err)
				}
				customRoleID = ptr(role.ID)
			}
			user.RoleID = ptr(*customRoleID)
		}
		if err := g.s.users.CreateAs(g.ctx, g.actor, user, p.DemoPassword); err != nil {
			return err
		}
		g.addStaff(user.ID, m.Role)
		g.userIDs[user.Email] = user.ID
		g.plan.at(&models.User{}, user.ID, g.start.Add(time.Duration(i+1)*time.Hour))
	}
	if len(g.sales) == 0 || len(g.support) == 0 {
		return fmt.Errorf("profile %s needs at least one sales and one support user", p.Name)
	}
	return nil
}

func (g *generator) addStaff(id uint, role models.UserRole) {
	g.staffIDs = append(g.staffIDs, id)
	if g.p.Storefront == nil || role != models.RoleCustom {
		g.taskStaff = append(g.taskStaff, id)
	}
	g.roleByID[id] = role
	switch role {
	case models.RoleSales:
		g.sales = append(g.sales, id)
	case models.RoleSupport:
		g.support = append(g.support, id)
	}
}

func (g *generator) genSettings() error {
	return g.s.config.Set("general.company_name", g.p.BusinessName)
}

func (g *generator) genLabels() error {
	for i, def := range g.p.Labels {
		label := &models.Label{Name: def.Name, Color: def.Color}
		if err := g.s.labels.Create(label); err != nil {
			return err
		}
		g.labelIDs = append(g.labelIDs, label.ID)
		g.plan.at(&models.Label{}, label.ID, g.start.Add(24*time.Hour+time.Duration(i)*time.Minute))
	}
	return nil
}

func (g *generator) genProducts() error {
	for i, def := range g.p.Products {
		product := &models.Product{Name: def.Name, SKU: def.SKU, Description: def.Description,
			UnitPriceCents: def.PriceCents, Currency: g.p.Currency, IsActive: true}
		if err := g.s.products.Create(product); err != nil {
			return err
		}
		g.productIDs = append(g.productIDs, product.ID)
		g.plan.at(&models.Product{}, product.ID, g.start.Add(24*time.Hour+time.Duration(i)*time.Minute))
	}
	return nil
}

// ---- companies, leads and customers ----------------------------------------

func (g *generator) genCompanies() error {
	employeeRanges := []string{"1-10", "11-50", "51-200", "201-500"}
	n := g.vol.Companies
	for i := 0; i < n; i++ {
		name := g.companyName(i)
		domain := slug(name, "-") + ".example"
		company := &models.Company{
			Name:          name,
			Domain:        domain,
			Website:       "https://www." + domain,
			Industry:      pickOne(g, g.p.Industries),
			EmployeeRange: pickOne(g, employeeRanges),
			Phone:         g.phone(),
			Address:       g.fake.Street(),
			City:          g.fake.City(),
			Country:       pickOne(g, g.p.Countries),
			PostalCode:    g.fake.Zip(),
			OwnerID:       ptr(pickOne(g, g.sales)),
		}
		if err := g.s.companies.Create(company); err != nil {
			return err
		}
		g.companies = append(g.companies, genCompany{id: company.ID, name: name, domain: domain})
		g.plan.at(&models.Company{}, company.ID, g.spread(i, n, g.start, g.start.AddDate(0, 4, 0)))
	}
	return nil
}

// convertedIndexes spreads the converted leads over the set; index 4 is the
// fifth status of the opening cycle.
func convertedIndexes(n, converted int) map[int]bool {
	out := map[int]bool{}
	if n == 0 || converted == 0 {
		return out
	}
	first := 4
	if first >= n {
		first = n - 1
	}
	step := (n - first) / converted
	if step < 1 {
		step = 1
	}
	for k := 0; k < converted && first+k*step < n; k++ {
		out[first+k*step] = true
	}
	return out
}

func (g *generator) genLeads() error {
	n := g.vol.Leads
	converted := g.vol.ConvertedLeads
	if converted > g.vol.Customers {
		converted = g.vol.Customers
	}
	convert := convertedIndexes(n, converted)
	open := []models.LeadStatus{models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusQualified, models.LeadStatusUnqualified}
	for i := 0; i < n; i++ {
		var status models.LeadStatus
		if convert[i] {
			// Converted through the service below, from a status the
			// leads.conversion.allowed_statuses default allows.
			status = models.LeadStatusQualified
		} else {
			status = cycleOrWeighted(g, i, open, []int{30, 30, 20, 20})
		}
		first, last := g.fake.FirstName(), g.fake.LastName()
		var companyID *uint
		var employer, domain string
		if g.chance(70) {
			c := pickOne(g, g.companies)
			companyID, employer, domain = ptr(c.id), c.name, c.domain
		} else {
			employer = g.companyName(len(g.companies) + g.intn(200))
			domain = slug(employer, "-") + ".example"
		}
		lead := &models.Lead{
			FirstName: first,
			LastName:  last,
			Email:     g.email(first, last, domain),
			Phone:     g.phone(),
			Company:   employer,
			CompanyID: companyID,
			Position:  pickOne(g, g.p.Positions),
			Source:    cycleOrWeighted(g, i, g.p.LeadSources, []int{40, 25, 15, 20}),
			Status:    status,
			Notes:     g.fake.Sentence(),
			OwnerID:   pickOne(g, g.sales),
		}
		if err := g.s.leads.Create(lead); err != nil {
			return err
		}
		created := g.history(i, n)
		rec := genLead{id: lead.ID, owner: lead.OwnerID, created: created, status: status}
		if convert[i] {
			customer, err := g.s.leads.ConvertToCustomer(g.ctx, lead.ID, &models.Customer{
				Website:    "https://www." + domain,
				Address:    g.fake.Street(),
				City:       g.fake.City(),
				Country:    pickOne(g, g.p.Countries),
				PostalCode: g.fake.Zip(),
				Notes:      "Converted from a " + lead.Source + " lead.",
			})
			if err != nil {
				return fmt.Errorf("converting lead %d: %w", lead.ID, err)
			}
			// A converted customer is never older than its lead.
			convertedAt := g.later(created, 48, 40*24)
			g.plan.stamp(&models.Lead{}, lead.ID, created, convertedAt)
			g.plan.at(&models.Customer{}, customer.ID, convertedAt)
			g.customers = append(g.customers, genCustomer{id: customer.ID, companyID: customer.CompanyID, created: convertedAt})
			rec.status = models.LeadStatusConverted
		} else {
			g.plan.stamp(&models.Lead{}, lead.ID, created, g.later(created, 0, 10*24))
		}
		g.leads = append(g.leads, rec)
	}
	return nil
}

func (g *generator) genCustomers() error {
	direct := g.vol.Customers - len(g.customers)
	if direct < 1 {
		direct = 1
	}
	// The customer login and its customer record, at the first company.
	home := g.companies[0]
	m := g.p.CustomerLogin
	loginEmail := g.email(m.First, m.Last, home.domain)
	user := &models.User{Email: loginEmail, FirstName: m.First, LastName: m.Last, Role: models.RoleCustomer, IsActive: true}
	if err := g.s.users.CreateAs(g.ctx, g.actor, user, g.p.DemoPassword); err != nil {
		return err
	}
	g.customerUser = user.ID
	g.roleByID[user.ID] = models.RoleCustomer
	g.plan.at(&models.User{}, user.ID, g.start.Add(time.Duration(len(g.p.Staff))*time.Hour))

	for i := 0; i < direct; i++ {
		var first, last, email string
		var companyID *uint
		var employer, domain string
		if i == 0 {
			first, last, email = m.First, m.Last, loginEmail
			companyID, employer, domain = ptr(home.id), home.name, home.domain
		} else {
			first, last = g.fake.FirstName(), g.fake.LastName()
			if g.chance(85) {
				c := pickOne(g, g.companies)
				companyID, employer, domain = ptr(c.id), c.name, c.domain
			} else {
				employer = g.companyName(len(g.companies) + g.intn(200))
				domain = slug(employer, "-") + ".example"
			}
			email = g.email(first, last, domain)
		}
		customer := &models.Customer{
			FirstName:    first,
			LastName:     last,
			Email:        email,
			Phone:        g.phone(),
			Company:      employer,
			Website:      "https://www." + domain,
			Position:     pickOne(g, g.p.Positions),
			Address:      g.fake.Street(),
			City:         g.fake.City(),
			Country:      pickOne(g, g.p.Countries),
			PostalCode:   g.fake.Zip(),
			Notes:        g.fake.Sentence(),
			CompanyID:    companyID,
			AssignedToID: ptr(pickOne(g, g.sales)),
		}
		if i == 0 {
			customer.UserID = ptr(g.customerUser)
		}
		if err := g.s.customers.Create(g.ctx, customer); err != nil {
			return err
		}
		created := g.history(i, direct)
		if i == 0 {
			created = g.start.Add(36 * time.Hour)
		}
		g.plan.at(&models.Customer{}, customer.ID, created)
		rec := genCustomer{id: customer.ID, companyID: customer.CompanyID, created: created}
		if i == 0 {
			g.login = rec
		}
		g.customers = append(g.customers, rec)
	}
	return nil
}

// ---- deals and quotes ------------------------------------------------------

var dealPath = map[models.DealStage][]models.DealStage{
	models.DealStageQualification: nil,
	models.DealStageProposal:      {models.DealStageProposal},
	models.DealStageNegotiation:   {models.DealStageProposal, models.DealStageNegotiation},
	models.DealStageWon:           {models.DealStageProposal, models.DealStageNegotiation, models.DealStageWon},
	models.DealStageLost:          {models.DealStageProposal, models.DealStageLost},
}

func (g *generator) genDealsAndQuotes() error {
	stages := []models.DealStage{models.DealStageQualification, models.DealStageProposal, models.DealStageNegotiation, models.DealStageWon, models.DealStageLost}
	n := g.vol.Deals
	span := (g.p.DealAmountMaxCents - g.p.DealAmountMinCents) / 10000
	for i := 0; i < n; i++ {
		target := cycleOrWeighted(g, i, stages, []int{25, 25, 20, 18, 12})
		customer := pickOne(g, g.customers)
		companyID := customer.companyID
		if companyID == nil {
			companyID = ptr(pickOne(g, g.companies).id)
		}
		companyName := g.companyNameByID(*companyID)
		deal := &models.Deal{
			Title:             fmt.Sprintf("%s %s for %s", pickOne(g, g.p.Platforms), pickOne(g, g.p.DealWorks), companyName),
			Stage:             models.DealStageQualification,
			AmountCents:       g.p.DealAmountMinCents + g.rnd.Int64N(span+1)*10000,
			Currency:          g.p.Currency,
			Source:            pickOne(g, g.p.LeadSources),
			Notes:             g.fake.Sentence(),
			CompanyID:         companyID,
			CustomerID:        ptr(customer.id),
			OwnerID:           pickOne(g, g.sales),
			ExpectedCloseDate: ptr(time.Now().UTC().AddDate(0, 2, 0)),
		}
		if err := g.s.deals.Create(deal, nil, g.admin); err != nil {
			return err
		}
		created := g.history(i, n)
		if created.Before(customer.created) {
			created = g.later(customer.created, 1, 72)
		}
		g.deals = append(g.deals, genDeal{id: deal.ID, owner: deal.OwnerID, customer: customer.id, target: target, created: created})
	}

	// Quotes are written while every deal is still open: the quote service
	// refuses a quote on a won or lost deal.
	if err := g.genQuotes(); err != nil {
		return err
	}

	for _, d := range g.deals {
		times := []time.Time{d.created}
		at := d.created
		for _, stage := range dealPath[d.target] {
			var reason *string
			if stage == models.DealStageLost {
				reason = ptr(pickOne(g, g.p.LostReasons))
			}
			if _, err := g.s.deals.ChangeStage(d.id, stage, nil, reason, g.admin); err != nil {
				return fmt.Errorf("deal %d to %s: %w", d.id, stage, err)
			}
			at = g.later(at, 3*24, 25*24)
			times = append(times, at)
		}
		cols := map[string]any{
			"created_at":          d.created,
			"updated_at":          at,
			"expected_close_date": d.created.AddDate(0, 0, 30+g.intn(90)),
		}
		if d.target.IsClosed() {
			cols["closed_at"] = at
		}
		g.plan.set(&models.Deal{}, d.id, cols)
		// A stage change is never older than its deal.
		g.plan.children(&models.DealStageChange{}, "deal_id", d.id, "changed_at", times)
	}
	return nil
}

func (g *generator) companyNameByID(id uint) string {
	for _, c := range g.companies {
		if c.id == id {
			return c.name
		}
	}
	return ""
}

func (g *generator) genQuotes() error {
	statuses := []models.QuoteStatus{models.QuoteStatusDraft, models.QuoteStatusSent, models.QuoteStatusAccepted, models.QuoteStatusRejected, models.QuoteStatusExpired}
	prefer := map[models.QuoteStatus]models.DealStage{
		models.QuoteStatusAccepted: models.DealStageWon,
		models.QuoteStatusRejected: models.DealStageLost,
	}
	n := g.vol.Quotes
	for i := 0; i < n; i++ {
		status := cycleOrWeighted(g, i, statuses, []int{20, 25, 25, 15, 15})
		quote := &models.Quote{
			Notes: "Prices exclude VAT unless stated.",
			// The service refuses to accept a quote past valid_until, so
			// the quote is valid in real time while it moves; the backdate
			// step then moves valid_until with the rest of its history.
			ValidUntil: ptr(time.Now().UTC().AddDate(0, 0, 45)),
		}
		var deal *genDeal
		if g.chance(60) {
			deal = g.dealFor(prefer[status])
		}
		created := g.history(i, n)
		if deal != nil {
			quote.DealID = ptr(deal.id)
			quote.OwnerID = deal.owner
			quote.Title = "Proposal: " + g.dealTitle(deal.id)
			if created.Before(deal.created) {
				created = g.later(deal.created, 24, 20*24)
			}
		} else {
			customer := pickOne(g, g.customers)
			quote.CustomerID = ptr(customer.id)
			quote.OwnerID = pickOne(g, g.sales)
			quote.Title = g.p.QuoteTitle + " " + fmt.Sprint(i+1)
			if created.Before(customer.created) {
				created = g.later(customer.created, 24, 10*24)
			}
		}
		if len(quote.Title) > 200 {
			quote.Title = quote.Title[:200]
		}
		if err := g.s.quotes.Create(quote, g.admin, models.RoleAdmin); err != nil {
			return err
		}
		lines := 1 + g.intn(4)
		order := g.rnd.Perm(len(g.productIDs))
		for l := 0; l < lines && l < len(order); l++ {
			discount := int64(0)
			if g.chance(25) {
				discount = int64(500 * (1 + g.intn(3)))
			}
			if _, err := g.s.quotes.AddLine(quote.ID, models.QuoteLineInput{
				ProductID:  g.productIDs[order[l]],
				Quantity:   models.QuoteQuantityFromUnits(int64(1 + g.intn(3))),
				DiscountBP: discount,
				TaxRateBP:  2000,
			}); err != nil {
				return fmt.Errorf("quote %d line: %w", quote.ID, err)
			}
		}

		times := []time.Time{created}
		cols := map[string]any{
			"created_at":  created,
			"updated_at":  created,
			"valid_until": created.AddDate(0, 0, 30),
		}
		if status != models.QuoteStatusDraft {
			if _, err := g.s.quotes.ChangeStatus(quote.ID, models.QuoteStatusSent, g.admin); err != nil {
				return fmt.Errorf("quote %d to sent: %w", quote.ID, err)
			}
			sent := g.later(created, 1, 72)
			times = append(times, sent)
			cols["sent_at"], cols["updated_at"] = sent, sent
			if status != models.QuoteStatusSent {
				if _, err := g.s.quotes.ChangeStatus(quote.ID, status, g.admin); err != nil {
					return fmt.Errorf("quote %d to %s: %w", quote.ID, status, err)
				}
				closed := g.later(sent, 24, 14*24)
				times = append(times, closed)
				cols["closed_at"], cols["updated_at"] = closed, closed
			}
		}
		g.plan.set(&models.Quote{}, quote.ID, cols)
		g.plan.children(&models.QuoteStatusChange{}, "quote_id", quote.ID, "changed_at", times)
		g.plan.children(&models.QuoteLine{}, "quote_id", quote.ID, "created_at", []time.Time{created})
		g.plan.children(&models.QuoteLine{}, "quote_id", quote.ID, "updated_at", []time.Time{created})
	}
	return nil
}

// dealFor picks a deal, preferring one whose final stage is want.
func (g *generator) dealFor(want models.DealStage) *genDeal {
	if want != "" {
		var matching []int
		for k := range g.deals {
			if g.deals[k].target == want {
				matching = append(matching, k)
			}
		}
		if len(matching) > 0 {
			return &g.deals[pickOne(g, matching)]
		}
	}
	return &g.deals[g.intn(len(g.deals))]
}

func (g *generator) dealTitle(id uint) string {
	var deal models.Deal
	if err := g.db.Select("title").First(&deal, id).Error; err != nil {
		return ""
	}
	return deal.Title
}

// ---- tickets, tasks and notifications --------------------------------------

func (g *generator) genTickets() error {
	statuses := []models.TicketStatus{models.TicketStatusOpen, models.TicketStatusInProgress, models.TicketStatusResolved, models.TicketStatusClosed}
	priorities := []models.TicketPriority{models.TicketPriorityLow, models.TicketPriorityMedium, models.TicketPriorityHigh, models.TicketPriorityUrgent}
	n := g.vol.Tickets
	for i := 0; i < n; i++ {
		customer := pickOne(g, g.customers)
		if i%10 == 0 {
			customer = g.login
		}
		status := cycleOrWeighted(g, i, statuses, []int{25, 25, 30, 20})
		assignee := pickOne(g, g.support)
		ticket := &models.Ticket{
			Title:        pickOne(g, g.p.TicketSubjects),
			Description:  g.fake.Paragraph(),
			Status:       status,
			Priority:     cycleOrWeighted(g, i, priorities, []int{25, 40, 25, 10}),
			CustomerID:   customer.id,
			AssignedToID: ptr(assignee),
		}
		if status == models.TicketStatusResolved || status == models.TicketStatusClosed {
			ticket.Resolution = pickOne(g, g.p.Resolutions)
		}
		if err := g.s.tickets.Create(ticket); err != nil {
			return err
		}
		created := g.history(i, n)
		if created.Before(customer.created) {
			created = g.later(customer.created, 1, 10*24)
		}

		comments := 1 + g.intn(8)
		at := created
		var firstResponse *time.Time
		for c := 0; c < comments; c++ {
			author := assignee
			switch r := g.intn(100); {
			case r < 15:
				author = g.admin
			case r < 25:
				author = pickOne(g, g.support)
			case r < 40 && customer.id == g.login.id:
				author = g.customerUser
			}
			internal := g.roleByID[author] != models.RoleCustomer && g.chance(25)
			comment := &models.TicketComment{
				TicketID:     ticket.ID,
				AuthorUserID: author,
				Body:         pickOne(g, g.p.CommentOpeners) + " " + g.fake.Sentence(),
				IsInternal:   internal,
			}
			if err := g.s.tickets.CreateComment(comment); err != nil {
				return err
			}
			// A comment is never older than its ticket.
			at = g.later(at, 1, 48)
			g.plan.at(&models.TicketComment{}, comment.ID, at)
			role := g.roleByID[author]
			if firstResponse == nil && !internal && (role == models.RoleAdmin || role == models.RoleSupport) {
				firstResponse = ptr(at)
			}
		}
		cols := map[string]any{"created_at": created, "updated_at": at}
		if firstResponse != nil {
			cols["first_responded_at"] = *firstResponse
		}
		var stored models.Ticket
		if err := g.db.Select("due_at").First(&stored, ticket.ID).Error; err != nil {
			return err
		}
		if stored.DueAt != nil {
			cols["due_at"] = created.Add(72 * time.Hour)
		}
		g.plan.set(&models.Ticket{}, ticket.ID, cols)
		g.tickets = append(g.tickets, genTicket{id: ticket.ID})
	}
	return nil
}

type taskKind int

const (
	taskOverduePending taskKind = iota
	taskOverdueInProgress
	taskUpcomingPending
	taskUpcomingInProgress
	taskCompleted
	taskCancelled
)

func (g *generator) genTasks() error {
	kinds := []taskKind{taskOverduePending, taskOverdueInProgress, taskUpcomingPending, taskUpcomingInProgress, taskCompleted, taskCancelled}
	priorities := []models.TaskPriority{models.TaskPriorityLow, models.TaskPriorityMedium, models.TaskPriorityHigh}
	var openLeads []genLead
	for _, l := range g.leads {
		if l.status != models.LeadStatusConverted {
			openLeads = append(openLeads, l)
		}
	}
	n := g.vol.Tasks
	for i := 0; i < n; i++ {
		kind := cycleOrWeighted(g, i, kinds, []int{12, 8, 25, 10, 38, 7})
		created := g.history(i, n)
		var due, updated time.Time
		var status models.TaskStatus
		switch kind {
		case taskOverduePending, taskOverdueInProgress:
			status = models.TaskStatusPending
			if kind == taskOverdueInProgress {
				status = models.TaskStatusInProgress
			}
			due = g.anchor.Add(-time.Duration(g.fake.IntRange(1, 60*24)) * time.Hour)
			if !created.Before(due) {
				created = due.Add(-time.Duration(g.fake.IntRange(2, 10*24)) * time.Hour)
			}
			updated = created
		case taskUpcomingPending, taskUpcomingInProgress:
			status = models.TaskStatusPending
			if kind == taskUpcomingInProgress {
				status = models.TaskStatusInProgress
			}
			due = g.anchor.Add(time.Duration(g.fake.IntRange(1, 45*24)) * time.Hour)
			updated = created
		case taskCompleted, taskCancelled:
			status = models.TaskStatusCompleted
			if kind == taskCancelled {
				status = models.TaskStatusCancelled
			}
			due = created.Add(time.Duration(g.fake.IntRange(24, 30*24)) * time.Hour)
			updated = g.later(created, 2, 20*24)
		}
		due = due.Truncate(time.Hour)
		task := &models.Task{
			Title:       pickOne(g, g.p.TaskTitles),
			Description: g.fake.Sentence(),
			Status:      status,
			Priority:    pickOne(g, priorities),
			DueDate:     ptr(due),
		}
		switch r := g.intn(100); {
		case r < 40 && len(openLeads) > 0:
			lead := pickOne(g, openLeads)
			task.LeadID, task.AssignedToID = ptr(lead.id), lead.owner
		case r < 70:
			task.CustomerID, task.AssignedToID = ptr(pickOne(g, g.customers).id), pickOne(g, g.taskStaff)
		case r < 90:
			deal := pickOne(g, g.deals)
			task.DealID, task.AssignedToID = ptr(deal.id), deal.owner
		default:
			task.AssignedToID = pickOne(g, g.taskStaff)
		}
		var labels []uint
		if g.chance(60) {
			order := g.rnd.Perm(len(g.labelIDs))
			for k := 0; k < 1+g.intn(2) && k < len(order); k++ {
				labels = append(labels, g.labelIDs[order[k]])
			}
		}
		if err := g.s.tasks.CreateWithLabels(task, labels, g.admin); err != nil {
			return err
		}
		g.plan.stamp(&models.Task{}, task.ID, created, updated)
	}
	return nil
}

func (g *generator) genNotifications() error {
	n := g.vol.Notifications
	recent := g.anchor.AddDate(0, 0, -60)
	for i := 0; i < n; i++ {
		var entityType string
		var entityID uint
		switch i % 3 {
		case 0:
			entityType, entityID = models.AutomationEntityLead, pickOne(g, g.leads).id
		case 1:
			entityType, entityID = models.AutomationEntityDeal, pickOne(g, g.deals).id
		default:
			entityType, entityID = models.AutomationEntityTicket, pickOne(g, g.tickets).id
		}
		notification := &models.Notification{
			UserID:     pickOne(g, g.staffIDs),
			Title:      pickOne(g, g.p.NotificationTitles),
			Body:       g.fake.Sentence(),
			EntityType: entityType,
			EntityID:   entityID,
		}
		// Notifications have no create service; this is the write path the
		// automation engine's notify step uses.
		if err := g.s.notifications.Create(notification); err != nil {
			return err
		}
		created := g.spread(i, n, recent, g.anchor.Add(-time.Hour))
		cols := map[string]any{"created_at": created, "updated_at": created}
		if i%5 < 2 {
			read := g.later(created, 1, 48)
			cols["read_at"], cols["updated_at"] = read, read
		}
		g.plan.set(&models.Notification{}, notification.ID, cols)
	}
	return nil
}

// ---- configuration records ---------------------------------------------------

func (g *generator) genRules() error {
	refs := RuleRefs{FormIDs: g.formIDs, UserIDs: g.userIDs}
	for i, rule := range g.p.Rules(refs) {
		rule := rule
		if err := g.s.automation.CreateRule(&rule, g.admin); err != nil {
			return fmt.Errorf("rule %q: %w", rule.Name, err)
		}
		at := g.start.Add(48*time.Hour + time.Duration(i)*time.Minute)
		g.plan.at(&models.AutomationRule{}, rule.ID, at)
		if g.p.Storefront != nil {
			// The steps are written with their rule; halvenmark leaves them
			// at the run time, as it always has.
			g.plan.children(&models.AutomationStep{}, "rule_id", rule.ID, "created_at", []time.Time{at})
		}
	}
	return nil
}

// genForms creates the profile's forms in order; the first is stamped 50
// hours into the history, each further one a minute later.
func (g *generator) genForms() error {
	for i, def := range g.p.Forms {
		form := def
		form.Fields = append([]models.FormFieldDef(nil), def.Fields...)
		form.AllowedDomains = append([]string(nil), def.AllowedDomains...)
		form.DefaultOwnerID = g.sales[0]
		if err := g.s.forms.Create(&form, g.admin); err != nil {
			return fmt.Errorf("form %q: %w", def.Name, err)
		}
		g.formIDs[form.Name] = form.ID
		g.forms[form.Name] = &form
		g.plan.at(&models.Form{}, form.ID, g.start.Add(50*time.Hour+time.Duration(i)*time.Minute))
	}
	return nil
}

func (g *generator) genWebhooks() error {
	for _, def := range g.p.Webhooks {
		created, err := g.s.webhooks.CreateEndpoint(g.ctx, webhook.WebhookEndpointInput{
			URL: def.URL, EventTypes: def.EventTypes, IsActive: ptr(false),
		})
		if err != nil {
			return fmt.Errorf("webhook endpoint: %w", err)
		}
		// The signing secret is returned once here and never printed.
		g.plan.at(&models.WebhookEndpoint{}, created.ID, g.start.Add(52*time.Hour))
	}
	return nil
}
