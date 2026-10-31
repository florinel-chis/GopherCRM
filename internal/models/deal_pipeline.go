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
	// ISO 4217 code of this total; one entry per currency present in the
	// stage.
	Currency string `json:"currency" example:"EUR"`
	// Sum of amount_cents of the stage's live deals in this currency, in
	// minor units.
	AmountCents int64 `json:"amount_cents" example:"1250000"`
	// Probability-weighted amount in minor units: round_half_up(sum of
	// amount_cents x probability / 100), integer arithmetic, rounded once on
	// the sum, never per deal.
	WeightedCents int64 `json:"weighted_cents" example:"500000"`
}

// DealPipelineStage is one column of the pipeline: how many live deals sit in
// the stage and their totals per currency (sorted by currency code; empty,
// never null, when the stage holds no deal).
type DealPipelineStage struct {
	// The stage this entry aggregates. The five stages always appear, in
	// pipeline order.
	Stage DealStage `json:"stage" example:"proposal"`
	// Number of live (not deleted) deals in the stage, over all currencies.
	Count int64 `json:"count" example:"3"`
	// One total per currency, sorted by currency code; an empty array, never
	// null, when the stage holds no deal.
	Totals []DealPipelineTotal `json:"totals"`
}

// DealPipeline is the payload of GET /deals/pipeline: every stage of
// DealStages, in pipeline order, empty ones included.
type DealPipeline struct {
	// All five stages in pipeline order (qualification, proposal,
	// negotiation, won, lost), empty ones included.
	Stages []DealPipelineStage `json:"stages"`
}

// DealWonTotal is the amount won in one currency.
type DealWonTotal struct {
	// ISO 4217 code of this total.
	Currency string `json:"currency" example:"EUR"`
	// Sum of amount_cents of the deals won in the period in this currency,
	// in minor units.
	AmountCents int64 `json:"amount_cents" example:"990000"`
}

// DealWonSummary counts the deals won in a period, with the amounts per
// currency (sorted by currency code; empty, never null).
type DealWonSummary struct {
	// Number of deals won in the period, over all currencies.
	Count int64 `json:"count" example:"2"`
	// The amounts won per currency, sorted by currency code; an empty array,
	// never null, when nothing was won.
	Totals []DealWonTotal `json:"totals"`
}

// DealDashboardPipeline is the payload of GET /dashboard/pipeline: the same
// stages as DealPipeline plus the deals won in the current calendar month
// (UTC).
type DealDashboardPipeline struct {
	// The pipeline stages, exactly as GET /deals/pipeline returns them for
	// the caller (a sales user's own deals only).
	Stages []DealPipelineStage `json:"stages"`
	// Deals whose closed_at falls in the current calendar month (UTC) and
	// that are in the won stage, for the same owner scope.
	WonThisMonth DealWonSummary `json:"won_this_month"`
}
