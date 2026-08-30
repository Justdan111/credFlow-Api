package analytics

import "time"

// Metric is one dashboard tile: a current value plus its movement against the
// previous comparable period.
type Metric struct {
	Value float64 `json:"value"`
	// Change is a percentage. It is nil when the previous period was zero —
	// a percentage change from zero is undefined, and reporting "+100%" would
	// be a fabrication that makes every new business look like it is booming.
	Change *float64 `json:"change"`
	// Direction is "up", "down" or "flat"; empty when Change is nil.
	Direction string `json:"direction,omitempty"`
}

// Summary backs the four tiles on the dashboard landing screen.
type Summary struct {
	Currency string `json:"currency"`

	Outstanding Metric `json:"outstanding"`

	Overdue struct {
		Metric
		// CustomerCount is how many distinct customers hold an overdue debt.
		CustomerCount int `json:"customerCount"`
		// NewCount is how many of those first fell overdue this month.
		NewCount int `json:"newCount"`
	} `json:"overdue"`

	Customers struct {
		Metric
		NewThisMonth int `json:"newThisMonth"`
	} `json:"customers"`

	Collected Metric `json:"collected"`
}

// RecentDebt is a row in the dashboard's "recent debts" list. It carries the
// customer name so the frontend does not have to resolve it per row.
type RecentDebt struct {
	ID           string  `json:"id"`
	CustomerID   string  `json:"customerId"`
	CustomerName string  `json:"customerName"`
	Amount       float64 `json:"amount"`
	DueDate      string  `json:"dueDate"` // YYYY-MM-DD
	Status       string  `json:"status"`
	// DaysOverdue is nil unless the debt is actually overdue.
	DaysOverdue *int `json:"daysOverdue"`
}

type RecentPayment struct {
	ID           string    `json:"id"`
	CustomerID   string    `json:"customerId"`
	CustomerName string    `json:"customerName"`
	Amount       float64   `json:"amount"`
	Method       string    `json:"method"`
	PaidAt       time.Time `json:"paidAt"`
}

// RiskBucket is the current distribution, computed live from customers rather
// than from snapshots — "now" is always accurate.
type RiskBucket struct {
	RiskLevel     string  `json:"riskLevel"`
	CustomerCount int     `json:"customerCount"`
	Percentage    float64 `json:"percentage"`
}

// TrendPoint is one month of the collections chart. Outstanding is the closing
// balance for that month, so the series is comparable point to point.
type TrendPoint struct {
	Month       string  `json:"month"` // YYYY-MM
	Label       string  `json:"label"` // "Jan"
	Collections float64 `json:"collections"`
	Outstanding float64 `json:"outstanding"`
}

type CollectionsTrend struct {
	Currency string       `json:"currency"`
	Points   []TrendPoint `json:"points"`
}

// CollectionRatePoint compares what was collected against the business's target.
// Target and Rate are nil when no target is set.
type CollectionRatePoint struct {
	Month  string   `json:"month"`
	Label  string   `json:"label"`
	Actual float64  `json:"actual"`
	Target *float64 `json:"target"`
	Rate   *float64 `json:"rate"` // percentage of target achieved
}

type CollectionRate struct {
	Currency string                `json:"currency"`
	Target   *float64              `json:"target"`
	Points   []CollectionRatePoint `json:"points"`
}

// RiskTrendPoint is one month of historical risk distribution, read from
// customer_risk_snapshots.
type RiskTrendPoint struct {
	Month  string `json:"month"`
	Label  string `json:"label"`
	Low    int    `json:"low"`
	Medium int    `json:"medium"`
	High   int    `json:"high"`
}

// RiskTrend carries meta so the frontend can tell a short series caused by
// young history apart from one caused by having no customers.
type RiskTrend struct {
	Points []RiskTrendPoint `json:"points"`
	Meta   struct {
		HistoryStartedAt *string `json:"historyStartedAt"` // YYYY-MM-DD, nil if no snapshots yet
		MonthsAvailable  int     `json:"monthsAvailable"`
	} `json:"meta"`
}

// Segment buckets customers by lifetime debt value. Bounds are returned
// explicitly so the frontend never hard-codes currency-specific thresholds.
type Segment struct {
	Label         string   `json:"label"`
	Min           float64  `json:"min"`
	Max           *float64 `json:"max"` // nil = unbounded
	CustomerCount int      `json:"customerCount"`
}

type CustomerSegments struct {
	Currency string    `json:"currency"`
	Segments []Segment `json:"segments"`
}
