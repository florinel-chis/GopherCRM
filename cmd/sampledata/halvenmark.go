package main

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// halvenmarkProfile is "Halvenmark Commerce Studio", a fictional agency that
// builds and runs online shops for small retailers. Every company, product,
// person and subject below is invented. The commerce platforms named
// (Magento 2, Adobe Commerce, Shopify, PrestaShop) appear only as the
// platform a service package or a deal concerns.
func halvenmarkProfile() *Profile {
	return &Profile{
		Name:         "halvenmark",
		BusinessName: "Halvenmark Commerce Studio",
		StaffDomain:  "halvenmark.example",
		DemoPassword: "Halvenmark-Demo-2026!",
		Staff: []StaffMember{
			{First: "Maren", Last: "Halloway", Role: models.RoleAdmin, Email: "admin@halvenmark.example"},
			{First: "Tobias", Last: "Wrenfield", Role: models.RoleAdmin},
			{First: "Ilse", Last: "Drummond", Role: models.RoleSales},
			{First: "Caspar", Last: "Ollerton", Role: models.RoleSales},
			{First: "Priya", Last: "Castellane", Role: models.RoleSales},
			{First: "Jonah", Last: "Featherby", Role: models.RoleSales},
			{First: "Odette", Last: "Marchbank", Role: models.RoleSales},
			{First: "Rafe", Last: "Tilbury", Role: models.RoleSupport},
			{First: "Senna", Last: "Holloway", Role: models.RoleSupport},
			{First: "Bram", Last: "Achterberg", Role: models.RoleSupport},
			{First: "Lucia", Last: "Penrose", Role: models.RoleSupport},
			{First: "Esme", Last: "Ravensworth", Role: models.RoleCustom},
			{First: "Dorian", Last: "Kettleby", Role: models.RoleCustom},
		},
		CustomerLogin: StaffMember{First: "Nadia", Last: "Okonkwo", Role: models.RoleCustomer},
		CustomRole: RoleDef{
			Name:        "Delivery lead",
			Description: "Runs project delivery: reads the pipeline, quotes and tickets and manages tasks.",
			Permissions: []service.RolePermissionInput{
				{Key: permissions.CustomersView, Scope: "all"},
				{Key: permissions.CompaniesView, Scope: "all"},
				{Key: permissions.DealsView, Scope: "all"},
				{Key: permissions.ProductsView, Scope: "all"},
				{Key: permissions.QuotesView, Scope: "all"},
				{Key: permissions.TicketsView, Scope: "all"},
				{Key: permissions.TasksView, Scope: "all"},
				{Key: permissions.TasksCreate, Scope: "all"},
				{Key: permissions.TasksUpdate, Scope: "all"},
				{Key: permissions.LabelsView, Scope: "all"},
			},
		},

		Currency:           "EUR",
		DealAmountMinCents: 3_000_00,
		DealAmountMaxCents: 180_000_00,

		Products: []ProductDef{
			{"Storefront Health Check", "HCS-HEALTH", "Two-day review of a live shop: errors, speed, checkout and security headers.", 3_500_00},
			{"Magento 2 Upgrade Sprint", "HCS-M2-UPG", "Upgrade of a Magento 2 shop to the current release, extensions included.", 12_000_00},
			{"Adobe Commerce Performance Audit", "HCS-AC-PERF", "Profiling and caching review of an Adobe Commerce installation.", 6_800_00},
			{"Shopify Theme Build", "HCS-SH-THEME", "Custom Shopify theme from approved designs.", 9_500_00},
			{"Shopify Checkout Extensions", "HCS-SH-CHK", "Checkout extensions and post-purchase pages for a Shopify shop.", 7_200_00},
			{"PrestaShop Module Development", "HCS-PS-MOD", "One custom PrestaShop module, specified, built and tested.", 4_800_00},
			{"Platform Migration Discovery", "HCS-MIG-DISC", "Workshop and written plan for moving a shop to another platform.", 5_500_00},
			{"Catalogue Data Clean-up", "HCS-CAT-CLEAN", "Attribute, category and image clean-up of an existing catalogue.", 2_900_00},
			{"Payment Provider Integration", "HCS-PAY-INT", "Integration and test of one payment provider.", 6_400_00},
			{"ERP Connector Setup", "HCS-ERP", "Order, stock and price synchronisation with the client's ERP.", 14_000_00},
			{"Monthly Care Plan (Standard)", "HCS-CARE-STD", "Monthly patching, monitoring and eight support hours.", 1_200_00},
			{"Monthly Care Plan (Priority)", "HCS-CARE-PRI", "Monthly patching, monitoring, twenty support hours and a four-hour response.", 2_600_00},
			{"Conversion Rate Review", "HCS-CRO", "Funnel analysis with a prioritised list of improvements.", 3_200_00},
			{"Accessibility Remediation Package", "HCS-A11Y", "Fixes for the findings of an accessibility audit of the storefront.", 5_900_00},
			{"Headless Storefront Prototype", "HCS-HEADLESS", "Clickable headless storefront prototype on the client's catalogue.", 18_500_00},
		},
		Labels: []LabelDef{
			{"Follow-up", "#1E88E5"},
			{"Urgent", "#E53935"},
			{"Upgrade", "#8E24AA"},
			{"Migration", "#00897B"},
			{"Care plan", "#43A047"},
			{"Billing", "#FB8C00"},
			{"Discovery", "#3949AB"},
			{"Waiting on client", "#757575"},
		},

		CompanyStems: []string{
			"Larkhollow", "Ashcombe", "Brindlecote", "Carrowmere", "Dovewick", "Fernhallow",
			"Gildersby", "Hollowbrook", "Ivystone", "Juniperfield", "Kestrelmoor", "Marrowdene",
			"Nettlecombe", "Oakharrow", "Pebblewick", "Quillbury", "Saltmere", "Thistlecombe",
			"Umberlea", "Vellacott", "Wrenfold", "Yarrowby", "Zennorvale", "Bramblewick",
		},
		CompanyTrades: []string{
			"Ceramics", "Outfitters", "Home Goods", "Tea Company", "Cycle Works", "Botanicals",
			"Pet Supply", "Kitchenware", "Leatherworks", "Toy Workshop", "Paper Goods", "Apparel",
		},
		Industries: []string{"Retail", "Home and garden", "Food and drink", "Fashion", "Sports and outdoors", "Gifts"},
		Countries:  []string{"United Kingdom", "Ireland", "Netherlands", "Germany", "United States"},

		LeadSources: []string{"website", "referral", "conference", "partner"},
		Positions: []string{
			"Founder", "Head of E-commerce", "Operations Manager", "Digital Lead",
			"Marketing Director", "Online Store Manager", "Managing Director",
		},

		Platforms: []string{"Magento 2", "Adobe Commerce", "Shopify", "PrestaShop"},
		DealWorks: []string{
			"replatforming", "upgrade", "checkout rebuild", "performance programme",
			"care plan renewal", "B2B portal", "catalogue migration", "payment integration",
		},
		LostReasons: []string{
			"Chose an in-house team", "Budget moved to next year", "Went with another agency",
			"Project paused by the client", "No reply after the proposal",
		},

		TicketSubjects: []string{
			"Checkout button unresponsive on mobile", "Order confirmation emails delayed",
			"Product images missing on category pages", "Tax totals rounding incorrectly",
			"Search returns no results for SKUs", "Admin sign-in loop after the upgrade",
			"Shipping rates missing for the EU zone", "Stock not syncing from the warehouse feed",
			"Discount code rejected at checkout", "Slow page loads during the sale",
			"Broken layout on the product comparison page", "Invoice PDF missing the VAT number",
			"Customer accounts locked after a password reset", "Sitemap not regenerating",
			"Cookie banner hides the basket button", "Duplicate orders from the payment callback",
			"Translations missing on checkout", "Gift card balance not shown",
			"Scheduled jobs stuck since the deployment", "Returns form not sending",
		},
		CommentOpeners: []string{
			"Thanks for the report.", "We reproduced this on staging.", "Quick update:",
			"Following up on this ticket.", "The fix is deployed to staging.",
			"Could you confirm the order number?", "We are still investigating.",
		},
		Resolutions: []string{
			"Patched the theme template and cleared the cache.", "Corrected the tax rule configuration.",
			"Restarted the queue consumers and added monitoring.", "Rolled back the extension update.",
			"Re-indexed the catalogue and fixed the feed mapping.",
		},

		TaskTitles: []string{
			"Prepare upgrade estimate", "Send discovery questionnaire", "Review staging deployment",
			"Schedule kickoff call", "Chase signed proposal", "Update care plan report",
			"Book performance test window", "Collect payment provider credentials", "Draft migration plan",
			"Confirm launch date", "Check backup restore", "Write release notes",
			"Review accessibility findings", "Follow up on overdue invoice", "Plan catalogue import",
			"Prepare monthly metrics", "Run checkout regression test", "Agree support hours for next quarter",
		},

		NotificationTitles: []string{
			"New lead assigned to you", "Deal moved forward in the pipeline",
			"Ticket waiting on your reply", "Quote accepted by the client", "Task due tomorrow",
		},

		QuoteTitle: "Service proposal",

		Forms: []models.Form{{
			Name:            "Project enquiry",
			Description:     "Enquiry form on the Halvenmark website.",
			Status:          models.FormStatusPublished,
			SubmitAction:    "message",
			ThankYouMessage: "Thanks, a member of the team will reply within one business day.",
			CreateLead:      true,
			Fields: []models.FormFieldDef{
				{Name: "first_name", Label: "First name", Type: "text", Required: true},
				{Name: "last_name", Label: "Last name", Type: "text", Required: true},
				{Name: "email", Label: "Work email", Type: "email", Required: true},
				{Name: "company", Label: "Shop name", Type: "text"},
				{Name: "notes", Label: "What do you need?", Type: "textarea"},
			},
			AllowedDomains: []string{"www.halvenmark.example"},
		}},
		Webhooks: []WebhookDef{
			{URL: "https://hooks.halvenmark.example/crm/leads", EventTypes: []string{"lead.created", "lead.updated"}},
			{URL: "https://hooks.halvenmark.example/crm/deals", EventTypes: []string{"deal.created", "deal.updated"}},
		},
		Rules: halvenmarkRules,

		Volumes: Volumes{
			Leads:          600,
			ConvertedLeads: 60,
			Customers:      250,
			Companies:      120,
			Deals:          180,
			Quotes:         140,
			Tickets:        400,
			Tasks:          800,
			Notifications:  60,
		},
	}
}

// halvenmarkRules are the three automation rules. Only the first is enabled,
// and it has no send_email step: a demo database never mails anyone.
func halvenmarkRules(RuleRefs) []models.AutomationRule {
	return []models.AutomationRule{
		{
			Name:        "Call back new leads",
			Description: "Creates a call-back task and an in-app notification for the owner of every new lead.",
			EntityType:  models.AutomationEntityLead,
			TriggerType: models.AutomationEventLeadCreated,
			Enabled:     true,
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
					Title: "Call back lead {{ lead.id }}", Priority: "high", DueInDays: 1,
					Recipient: models.AutomationRecipientOwner,
				}},
				{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
					Title: "New lead {{ lead.first_name }} {{ lead.last_name }}", Recipient: models.AutomationRecipientOwner,
				}},
			},
		},
		{
			Name:        "Email the owner when a deal is won",
			Description: "Disabled: a demo database sends no mail.",
			EntityType:  models.AutomationEntityDeal,
			TriggerType: models.AutomationEventDealStageChanged,
			Trigger:     models.AutomationTrigger{To: string(models.DealStageWon)},
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
					Subject: "Deal {{ deal.title }} is won", Body: "Please schedule the kickoff call.",
					Recipient: models.AutomationRecipientOwner,
				}},
			},
		},
		{
			Name:        "Nudge stalled proposals",
			Description: "Daily pass over proposals that have not moved for two weeks.",
			EntityType:  models.AutomationEntityDeal,
			TriggerType: models.AutomationTriggerSchedule,
			Trigger: models.AutomationTrigger{
				HourUTC: 8, StaleDays: 14,
				Filters: &models.AutomationScheduleFilter{Stage: string(models.DealStageProposal)},
			},
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
					Title: "Proposal {{ deal.title }} has stalled", Recipient: models.AutomationRecipientOwner,
				}},
			},
		},
	}
}
