package models

// DealPipelineRow is one (stage, currency) group of live deals as the
// repository aggregates it in SQL: the number of deals, the sum of their
// amounts and the sum of amount_cents × probability. The last one is still
// in cent-percent; the service divides it by 100 once per group, so the
// rounding happens on the total and never per deal.
type DealPipelineRow struct {
	Stage                  DealStage `gorm:"column:stage"`
	Currency               string    `gorm:"column:currency"`
	DealCount              int64     `gorm:"column:deal_count"`
	AmountCents            int64     `gorm:"column:amount_sum"`
	AmountTimesProbability int64     `gorm:"column:weighted_sum"`
}

// DealWonRow is one currency group of the deals won in a time range.
type DealWonRow struct {
	Currency    string `gorm:"column:currency"`
	DealCount   int64  `gorm:"column:deal_count"`
	AmountCents int64  `gorm:"column:amount_sum"`
}

// DealPipelineTotal is the money of one stage in one currency. Amounts in
// different currencies are never added together.
type DealPipelineTotal struct {
	Currency string `json:"currency" example:"EUR"`
	// AmountCents is the sum of amount_cents of the stage's deals in this
	// currency.
	AmountCents int64 `json:"amount_cents" example:"1250000"`
	// WeightedCents is round_half_up(Σ amount_cents × probability / 100),
	// rounded once on the sum.
	WeightedCents int64 `json:"weighted_cents" example:"500000"`
}

// DealPipelineStage is one column of the pipeline: how many live deals sit in
// the stage and their totals per currency (sorted by currency code; empty,
// never null, when the stage holds no deal).
type DealPipelineStage struct {
	Stage  DealStage           `json:"stage" example:"proposal"`
	Count  int64               `json:"count" example:"3"`
	Totals []DealPipelineTotal `json:"totals"`
}

// DealPipeline is the payload of GET /deals/pipeline: every stage of
// DealStages, in pipeline order, empty ones included.
type DealPipeline struct {
	Stages []DealPipelineStage `json:"stages"`
}

// DealWonTotal is the amount won in one currency.
type DealWonTotal struct {
	Currency    string `json:"currency" example:"EUR"`
	AmountCents int64  `json:"amount_cents" example:"990000"`
}

// DealWonSummary counts the deals won in a period, with the amounts per
// currency (sorted by currency code; empty, never null).
type DealWonSummary struct {
	Count  int64          `json:"count" example:"2"`
	Totals []DealWonTotal `json:"totals"`
}

// DealDashboardPipeline is the payload of GET /dashboard/pipeline: the same
// stages as DealPipeline plus the deals won in the current calendar month
// (UTC).
type DealDashboardPipeline struct {
	Stages       []DealPipelineStage `json:"stages"`
	WonThisMonth DealWonSummary      `json:"won_this_month"`
}
