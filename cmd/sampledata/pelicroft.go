package main

import (
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/permissions"
	"github.com/florinel-chis/gophercrm/internal/service"
)

// Names the pelicroft rules and the storefront steps refer to.
const (
	pelicroftContactForm    = "Contact us"
	pelicroftWholesaleForm  = "Wholesale enquiry"
	pelicroftNewsletterForm = "Newsletter"
	pelicroftStoreManager   = "linnea.ashworth@pelicroft.example"
	pelicroftTriageDueDays  = 2
)

// pelicroftContactTopics are the options of the contact form's topic field.
var pelicroftContactTopics = []string{"Order status", "Return or exchange", "Damaged item", "Product question", "Other"}

// pelicroftProfile is "Pelicroft Home", a fictional online shop for
// kitchenware, table linen, bedding, candles and decor. Its shoppers write in
// through the "Contact us" form, boutiques through "Wholesale enquiry" and
// readers sign up through "Newsletter"; every submission goes through the
// public submission path (storefront.go). Every person, boutique, product and
// message below is invented.
func pelicroftProfile() *Profile {
	return &Profile{
		Name:         "pelicroft",
		BusinessName: "Pelicroft Home",
		StaffDomain:  "pelicroft.example",
		DemoPassword: "Pelicroft-Demo-2026!",
		Staff: []StaffMember{
			{First: "Rosalind", Last: "Achery", Role: models.RoleAdmin, Email: "admin@pelicroft.example"},
			{First: "Linnea", Last: "Ashworth", Role: models.RoleCustom, Email: pelicroftStoreManager},
			{First: "Tamsin", Last: "Greaves", Role: models.RoleSupport},
			{First: "Elio", Last: "Marchetti", Role: models.RoleSupport},
			{First: "Hanne", Last: "Vestergaard", Role: models.RoleSupport},
			{First: "Ruairi", Last: "Delaney", Role: models.RoleSupport},
			{First: "Bastien", Last: "Morrow", Role: models.RoleSales},
		},
		CustomerLogin: StaffMember{First: "Margit", Last: "Lindqvist", Role: models.RoleCustomer},
		CustomRole: RoleDef{
			Name:        "Store manager",
			Description: "Runs the shop day to day: reads customers, wholesale deals, products, quotes and tickets and manages tasks.",
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
			},
		},

		Currency:           "EUR",
		DealAmountMinCents: 800_00,
		DealAmountMaxCents: 24_000_00,

		// Wholesale prices per unit, excluding VAT.
		Products: []ProductDef{
			{"Stoneware Mixing Bowl, Large", "PHW-KIT-001", "Glazed stoneware bowl, 28 cm, oven and dishwasher safe.", 18_50},
			{"Enamel Saucepan, 18 cm", "PHW-KIT-002", "Cream enamel saucepan with a beech handle.", 24_00},
			{"Olive Wood Spoon Set", "PHW-KIT-003", "Three oiled olive wood spoons.", 9_80},
			{"Linen Tea Towel, Natural", "PHW-KIT-004", "Stonewashed linen tea towel, 50 x 70 cm.", 6_40},
			{"Cast Iron Skillet, 26 cm", "PHW-KIT-005", "Pre-seasoned cast iron skillet.", 29_00},
			{"Glass Storage Jar, 1 l", "PHW-KIT-006", "Clip-top glass jar with a rubber seal.", 5_20},
			{"Ceramic Butter Dish", "PHW-KIT-007", "Matte white butter dish with a lid.", 11_90},
			{"Marble Pestle and Mortar", "PHW-KIT-008", "Honed white marble, 13 cm.", 16_30},
			{"Copper Measuring Cups", "PHW-KIT-009", "Set of four copper-plated measuring cups.", 14_70},
			{"Beechwood Chopping Board", "PHW-KIT-010", "End-grain beech board, 40 x 25 cm.", 21_00},
			{"Linen Tablecloth, Sage, 140 x 250", "PHW-TAB-001", "Washed linen tablecloth for six to eight places.", 38_00},
			{"Linen Napkins, Set of Four, Oat", "PHW-TAB-002", "Washed linen napkins, 45 x 45 cm.", 15_60},
			{"Cotton Table Runner, Indigo Stripe", "PHW-TAB-003", "Woven cotton runner, 40 x 180 cm.", 12_80},
			{"Rattan Placemats, Set of Four", "PHW-TAB-004", "Hand-woven round rattan placemats.", 17_40},
			{"Stoneware Dinner Plate, Speckled", "PHW-TAB-005", "Reactive glaze dinner plate, 27 cm.", 8_90},
			{"Stoneware Side Plate, Speckled", "PHW-TAB-006", "Reactive glaze side plate, 21 cm.", 6_70},
			{"Hand-blown Tumbler, Smoke", "PHW-TAB-007", "Hand-blown glass tumbler, 300 ml.", 5_90},
			{"Brass Cutlery Set, 16 Pieces", "PHW-TAB-008", "Brushed brass cutlery for four.", 46_00},
			{"Linen Duvet Cover, Double, Clay", "PHW-BED-001", "Stonewashed linen duvet cover, 200 x 200 cm.", 62_00},
			{"Linen Pillowcases, Pair, Clay", "PHW-BED-002", "Stonewashed linen pillowcases, 50 x 75 cm.", 19_50},
			{"Percale Fitted Sheet, Double, White", "PHW-BED-003", "Cotton percale fitted sheet, 140 x 200 cm.", 23_80},
			{"Waffle Blanket, Oatmeal", "PHW-BED-004", "Cotton waffle-weave blanket, 150 x 200 cm.", 34_00},
			{"Wool Throw, Herringbone", "PHW-BED-005", "Lambswool throw, 130 x 180 cm.", 41_50},
			{"Down-alternative Pillow, Medium", "PHW-BED-006", "Washable microfibre pillow, medium support.", 13_60},
			{"Muslin Baby Blanket, Sand", "PHW-BED-007", "Four-layer cotton muslin blanket.", 16_90},
			{"Soy Candle, Fig and Cedar, 200 g", "PHW-CAN-001", "Soy wax candle in an amber jar, 40 hours.", 8_20},
			{"Soy Candle, Sea Salt, 200 g", "PHW-CAN-002", "Soy wax candle in a frosted jar, 40 hours.", 8_20},
			{"Beeswax Taper Candles, Pair", "PHW-CAN-003", "Hand-dipped beeswax tapers, 25 cm.", 6_10},
			{"Pillar Candle, Ivory, 15 cm", "PHW-CAN-004", "Unscented pillar candle.", 4_90},
			{"Ceramic Candle Holder, Ribbed", "PHW-CAN-005", "Matte ceramic holder for taper candles.", 7_30},
			{"Reed Diffuser, Linen Fresh", "PHW-CAN-006", "100 ml reed diffuser with rattan reeds.", 10_40},
			{"Jute Basket, Medium", "PHW-DEC-001", "Hand-braided jute storage basket.", 14_20},
			{"Stoneware Vase, Tall, Moss", "PHW-DEC-002", "Glazed stoneware vase, 30 cm.", 19_80},
			{"Linen Cushion Cover, Rust", "PHW-DEC-003", "Linen cushion cover with a hidden zip, 50 x 50 cm.", 11_20},
			{"Wall Mirror, Round Oak Frame", "PHW-DEC-004", "Oak-framed round mirror, 60 cm.", 58_00},
			{"Cotton Rug, Ecru Fringe, 120 x 180", "PHW-DEC-005", "Flat-woven cotton rug with fringed ends.", 44_00},
			{"Ceramic Plant Pot, Terracotta", "PHW-DEC-006", "Unglazed terracotta pot with a saucer, 18 cm.", 7_60},
			{"Picture Frame, Walnut, A4", "PHW-DEC-007", "Walnut frame with a white mount.", 12_40},
			{"Woven Wall Hanging, Natural", "PHW-DEC-008", "Hand-woven cotton wall hanging, 50 x 80 cm.", 26_50},
			{"Glass Lantern, Brass Top", "PHW-DEC-009", "Clear glass lantern for pillar candles.", 22_30},
		},
		Labels: []LabelDef{
			{"Returns", "#E53935"},
			{"Courier", "#1E88E5"},
			{"Supplier", "#8E24AA"},
			{"Wholesale", "#00897B"},
			{"Stock", "#43A047"},
			{"Refund", "#FB8C00"},
			{"Photography", "#3949AB"},
			{"Waiting on customer", "#757575"},
		},

		CompanyStems: []string{
			"Brackenlea", "Corriemoss", "Dunmarrow", "Elderwick", "Foxhollow", "Glimmerdale",
			"Heronsby", "Inglewyn", "Kittlewood", "Lindenmere", "Mossbrae", "Nettlefold",
			"Orchardine", "Puddlestone", "Quincewell", "Rookhaven", "Sorrelby", "Tansywell",
			"Umbercott", "Wickerley",
		},
		CompanyTrades: []string{
			"Interiors", "Home Store", "Living", "Kitchen Shop", "Linen Room",
			"Candle Company", "Gift House", "Concept Store", "Homewares", "Table and Hearth",
		},
		Industries: []string{"Home decor retail", "Kitchenware retail", "Gift shop", "Interior design", "Hospitality"},
		Countries: []string{
			"Germany", "France", "Netherlands", "Belgium", "Ireland", "Austria",
			"Denmark", "Sweden", "Italy", "Spain", "Portugal", "Finland",
		},

		LeadSources: []string{"wholesale form", "trade fair", "referral"},
		Positions:   []string{"Owner", "Buyer", "Store Manager", "Founder"},

		Platforms:   []string{"Opening order of", "Seasonal restock of", "Holiday range of", "Standing order of"},
		DealWorks:   []string{"kitchenware", "table linen", "bedding", "candles", "decor"},
		LostReasons: []string{"Chose another supplier", "Minimum order too high", "Shop closed for refurbishment", "No reply after the quote"},

		CommentOpeners: []string{
			"Thanks for getting in touch.", "Sorry about this.", "Quick update:",
			"We have checked with the courier.", "Following up on your message.",
			"Could you send a photo of the item?", "Internal: waiting on the warehouse.",
		},
		Resolutions: []string{
			"Replacement sent with tracked delivery.", "Refund issued to the original payment method.",
			"Return label emailed and the exchange shipped.", "Delivery confirmed by the courier.",
			"Answered the product question and linked the care guide.",
		},

		TaskTitles: []string{
			"Restock linen napkins", "Photograph the new candle range", "Chase courier about late parcels",
			"Update the returns policy page", "Prepare the holiday gift guide", "Reply to wholesale price request",
			"Check damaged stock in the warehouse", "Reorder stoneware plates", "Plan the newsletter for next month",
			"Review supplier invoices", "Pack the trade fair samples", "Update product care instructions",
		},
		NotificationTitles: []string{"New wholesale enquiry"},

		QuoteTitle: "Wholesale quote",

		Forms: []models.Form{
			{
				Name:            pelicroftContactForm,
				Description:     "Customer service form on the Pelicroft Home website.",
				Status:          models.FormStatusPublished,
				SubmitAction:    "message",
				ThankYouMessage: "Thanks. Please confirm your email address from the message we just sent, and we will reply within one working day.",
				CreateTicket:    true,
				DoubleOptIn:     true,
				// Every generated submission sets a topic, so every ticket
				// reads "Contact us: <topic>" (#804).
				TicketTitleField: "topic",
				Fields: []models.FormFieldDef{
					{Name: "name", Label: "Your name", Type: "text", Required: true},
					{Name: "email", Label: "Email", Type: "email", Required: true},
					{Name: "order_number", Label: "Order number", Type: "text"},
					{Name: "topic", Label: "Topic", Type: "select", Options: pelicroftContactTopics},
					{Name: "message", Label: "Message", Type: "textarea", Required: true},
				},
				AllowedDomains: []string{"www.pelicroft.example", "localhost:8099"},
			},
			{
				Name:            pelicroftWholesaleForm,
				Description:     "Trade enquiry form for shops and interior designers.",
				Status:          models.FormStatusPublished,
				SubmitAction:    "message",
				ThankYouMessage: "Thanks, our wholesale team will send the trade price list within two working days.",
				CreateLead:      true,
				Fields: []models.FormFieldDef{
					{Name: "name", Label: "Your name", Type: "text", Required: true},
					{Name: "email", Label: "Business email", Type: "email", Required: true},
					{Name: "company", Label: "Shop or studio name", Type: "text", Required: true},
					{Name: "phone", Label: "Phone", Type: "phone"},
					{Name: "message", Label: "What would you like to stock?", Type: "textarea"},
				},
				AllowedDomains: []string{"www.pelicroft.example"},
			},
			{
				Name:            pelicroftNewsletterForm,
				Description:     "Newsletter sign-up in the website footer.",
				Status:          models.FormStatusPublished,
				SubmitAction:    "message",
				ThankYouMessage: "Thanks for signing up.",
				Fields: []models.FormFieldDef{
					{Name: "email", Label: "Email", Type: "email", Required: true},
					{Name: "first_name", Label: "First name", Type: "text"},
				},
				AllowedDomains: []string{"www.pelicroft.example"},
			},
		},
		Rules: pelicroftRules,

		Storefront: &Storefront{
			Origin:         "https://www.pelicroft.example",
			SpamOrigin:     "https://bulk-offers.example.net",
			ShopperDomains: []string{"example.com", "example.org", "example.net"},
			ContactPages:   []string{"/help/contact", "/orders/track", "/help/returns", "/products/stoneware-mixing-bowl"},
			WholesalePages: []string{"/trade", "/trade/price-list"},
			NewsletterPages: []string{
				"/", "/journal/setting-a-winter-table", "/collections/candles", "/collections/table-linen",
			},
			UserAgents: []string{
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
				"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
				"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
				"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Mobile Safari/537.36",
			},
			BotAgents: []string{"python-requests/2.31", "Go-http-client/1.1", "curl/8.4"},
			Messages: map[string][]string{
				"Order status": {
					"My order has not arrived yet and the tracking has not changed for four days.",
					"Could you tell me when my order will be dispatched?",
					"The tracking page says delivered but nothing came.",
				},
				"Return or exchange": {
					"I would like to exchange the duvet cover for the king size.",
					"The colour of the cushion cover is not what I expected; how do I return it?",
					"Please send a return label for the rug.",
				},
				"Damaged item": {
					"The vase arrived cracked; I have kept the packaging.",
					"Two of the plates were broken in the box.",
					"The candle jar was chipped on arrival.",
				},
				"Product question": {
					"Is the linen tablecloth pre-washed, and will it shrink?",
					"Can the cast iron skillet be used on an induction hob?",
					"Do you restock the speckled side plates?",
				},
				"Other": {
					"Do you offer gift wrapping?",
					"Can I change the delivery address of my order?",
					"I would like to update the email address on my account.",
				},
			},
			SpamMessages: []string{
				"Bulk offers for your shop, reply for the price list.",
				"We can boost your rankings in one week.",
				"Limited offer on wholesale packaging, order now.",
			},
			WholesaleMessages: []string{
				"We run a small homewares shop and would like your trade price list.",
				"Interested in stocking your candles and table linen for the holiday season.",
				"Could you send minimum order quantities for the stoneware range?",
				"We furnish holiday rentals and are looking for bedding in bulk.",
			},
		},

		Volumes: Volumes{
			ConvertedLeads: 40,
			Customers:      3000,
			Companies:      60,
			Deals:          45,
			Quotes:         40,
			Tasks:          400,

			ContactSubmissions: 1500,
			WholesaleEnquiries: 150,
			NewsletterSignups:  300,
		},
	}
}

// pelicroftRules are the three automation rules. The two enabled rules have
// only create_task and notify steps; the send_email rule is disabled. Ticket
// rules can test only status, priority, owner and customer, never the topic
// of the submission behind a ticket, so the store manager triages every
// contact-form ticket instead of only the damaged-item ones.
func pelicroftRules(refs RuleRefs) []models.AutomationRule {
	return []models.AutomationRule{
		{
			Name:        "Triage contact-form tickets",
			Description: "Gives the store manager a triage task for every new ticket from the contact form.",
			EntityType:  models.AutomationEntityTicket,
			TriggerType: models.AutomationEventTicketCreated,
			Enabled:     true,
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepCreateTask, Config: models.AutomationStepConfig{
					Title:       "Triage ticket {{ ticket.id }}",
					Description: "Read the submission behind ticket {{ ticket.id }}, then set its priority and assignee.",
					Priority:    "medium", DueInDays: pelicroftTriageDueDays,
					Recipient: models.AutomationRecipientUser, UserID: refs.UserIDs[pelicroftStoreManager],
				}},
			},
		},
		{
			Name:        "Tell wholesale about new enquiries",
			Description: "Notifies the owner of a lead created by the wholesale enquiry form.",
			EntityType:  models.AutomationEntityLead,
			TriggerType: models.AutomationEventLeadFormSubmitted,
			Trigger:     models.AutomationTrigger{FormID: refs.FormIDs[pelicroftWholesaleForm]},
			Enabled:     true,
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepNotify, Config: models.AutomationStepConfig{
					Title: "New wholesale enquiry from {{ lead.company }}", Recipient: models.AutomationRecipientOwner,
				}},
			},
		},
		{
			Name:        "Email the assignee when a ticket is resolved",
			Description: "Disabled: a demo database sends no mail.",
			EntityType:  models.AutomationEntityTicket,
			TriggerType: models.AutomationEventTicketStatusChanged,
			Trigger:     models.AutomationTrigger{To: string(models.TicketStatusResolved)},
			Steps: []models.AutomationStep{
				{StepType: models.AutomationStepSendEmail, Config: models.AutomationStepConfig{
					Subject: "Ticket {{ ticket.number }} is resolved", Body: "Please check that the customer has what they need.",
					Recipient: models.AutomationRecipientOwner,
				}},
			},
		},
	}
}
