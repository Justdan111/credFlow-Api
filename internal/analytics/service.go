package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

var ErrValidation = errors.New("validation failed")

const (
	defaultMonths = 6
	maxMonths     = 24
	defaultLimit  = 5
	maxLimit      = 20
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// Summary builds the four dashboard tiles.
//
// Two different comparison shapes are in play. Outstanding, overdue and customer
// count are point-in-time balances, so they compare today against the last day
// of the previous month. Collected is a flow, so it compares this month's total
// against last month's.
func (s *Service) Summary(ctx context.Context, businessID string) (Summary, error) {
	var out Summary

	settings, err := s.repo.GetBusinessSettings(ctx, businessID)
	if err != nil {
		return out, fmt.Errorf("load business settings: %w", err)
	}
	out.Currency = settings.Currency

	now := time.Now()
	thisMonthStart := startOfMonth(now)
	lastMonthStart := thisMonthStart.AddDate(0, -1, 0)
	// The last instant of the previous month, used as the "before" snapshot.
	prevCutoff := thisMonthStart.AddDate(0, 0, -1)
	today := now

	// --- Outstanding: balance now vs balance at end of last month ---
	curOutstanding, err := s.repo.OutstandingAt(ctx, businessID, today)
	if err != nil {
		return out, fmt.Errorf("outstanding now: %w", err)
	}
	prevOutstanding, err := s.repo.OutstandingAt(ctx, businessID, prevCutoff)
	if err != nil {
		return out, fmt.Errorf("outstanding previous: %w", err)
	}
	out.Outstanding = newMetric(curOutstanding, prevOutstanding)

	// --- Overdue ---
	curOverdue, overdueCustomers, newlyOverdue, err := s.repo.OverdueAt(ctx, businessID, today, thisMonthStart)
	if err != nil {
		return out, fmt.Errorf("overdue now: %w", err)
	}
	prevOverdue, _, _, err := s.repo.OverdueAt(ctx, businessID, prevCutoff, lastMonthStart)
	if err != nil {
		return out, fmt.Errorf("overdue previous: %w", err)
	}
	out.Overdue.Metric = newMetric(curOverdue, prevOverdue)
	out.Overdue.CustomerCount = overdueCustomers
	out.Overdue.NewCount = newlyOverdue

	// --- Customers ---
	curCustomers, err := s.repo.CustomerCountAt(ctx, businessID, today)
	if err != nil {
		return out, fmt.Errorf("customers now: %w", err)
	}
	prevCustomers, err := s.repo.CustomerCountAt(ctx, businessID, prevCutoff)
	if err != nil {
		return out, fmt.Errorf("customers previous: %w", err)
	}
	newThisMonth, err := s.repo.CustomersCreatedBetween(ctx, businessID, thisMonthStart, thisMonthStart.AddDate(0, 1, 0))
	if err != nil {
		return out, fmt.Errorf("new customers: %w", err)
	}
	out.Customers.Metric = newMetric(float64(curCustomers), float64(prevCustomers))
	out.Customers.NewThisMonth = newThisMonth

	// --- Collected: a flow, so month against month ---
	curCollected, err := s.repo.CollectedBetween(ctx, businessID, thisMonthStart, thisMonthStart.AddDate(0, 1, 0))
	if err != nil {
		return out, fmt.Errorf("collected this month: %w", err)
	}
	prevCollected, err := s.repo.CollectedBetween(ctx, businessID, lastMonthStart, thisMonthStart)
	if err != nil {
		return out, fmt.Errorf("collected last month: %w", err)
	}
	out.Collected = newMetric(curCollected, prevCollected)

	return out, nil
}

func (s *Service) RecentDebts(ctx context.Context, businessID string, limit int) ([]RecentDebt, error) {
	return s.repo.RecentDebts(ctx, businessID, clampLimit(limit))
}

func (s *Service) RecentPayments(ctx context.Context, businessID string, limit int) ([]RecentPayment, error) {
	return s.repo.RecentPayments(ctx, businessID, clampLimit(limit))
}

func (s *Service) RiskDistribution(ctx context.Context, businessID string) ([]RiskBucket, error) {
	return s.repo.RiskDistribution(ctx, businessID)
}

// CollectionsTrend pairs each month's collections with its closing outstanding
// balance, so the two series on the chart are directly comparable.
func (s *Service) CollectionsTrend(ctx context.Context, businessID string, months int) (CollectionsTrend, error) {
	months = clampMonths(months)
	var out CollectionsTrend

	settings, err := s.repo.GetBusinessSettings(ctx, businessID)
	if err != nil {
		return out, fmt.Errorf("load business settings: %w", err)
	}
	out.Currency = settings.Currency

	from := startOfMonth(time.Now()).AddDate(0, -(months - 1), 0)
	collections, err := s.repo.MonthlyCollections(ctx, businessID, from, months)
	if err != nil {
		return out, fmt.Errorf("monthly collections: %w", err)
	}

	out.Points = make([]TrendPoint, 0, months)
	for i := 0; i < months; i++ {
		monthStart := from.AddDate(0, i, 0)
		key := monthStart.Format("2006-01")

		// Closing balance: the last day this month was live.
		closing := monthStart.AddDate(0, 1, -1)
		outstanding, err := s.repo.OutstandingAt(ctx, businessID, closing)
		if err != nil {
			return out, fmt.Errorf("outstanding for %s: %w", key, err)
		}

		out.Points = append(out.Points, TrendPoint{
			Month:       key,
			Label:       monthLabel(key),
			Collections: collections[key],
			Outstanding: outstanding,
		})
	}
	return out, nil
}

// CollectionRate compares monthly collections against the business's target.
// Target and Rate stay nil when no target is set — the API must not invent a
// goal on the business's behalf.
func (s *Service) CollectionRate(ctx context.Context, businessID string, months int) (CollectionRate, error) {
	months = clampMonths(months)
	var out CollectionRate

	settings, err := s.repo.GetBusinessSettings(ctx, businessID)
	if err != nil {
		return out, fmt.Errorf("load business settings: %w", err)
	}
	out.Currency = settings.Currency
	out.Target = settings.Target

	from := startOfMonth(time.Now()).AddDate(0, -(months - 1), 0)
	collections, err := s.repo.MonthlyCollections(ctx, businessID, from, months)
	if err != nil {
		return out, fmt.Errorf("monthly collections: %w", err)
	}

	out.Points = make([]CollectionRatePoint, 0, months)
	for i := 0; i < months; i++ {
		key := from.AddDate(0, i, 0).Format("2006-01")
		p := CollectionRatePoint{
			Month:  key,
			Label:  monthLabel(key),
			Actual: collections[key],
			Target: settings.Target,
		}
		// A rate against a zero target is undefined, not 0% and not infinite.
		if settings.Target != nil && *settings.Target > 0 {
			rate := round1(p.Actual * 100 / *settings.Target)
			p.Rate = &rate
		}
		out.Points = append(out.Points, p)
	}
	return out, nil
}

func (s *Service) RiskTrend(ctx context.Context, businessID string, months int) (RiskTrend, error) {
	months = clampMonths(months)
	var out RiskTrend

	from := startOfMonth(time.Now()).AddDate(0, -(months - 1), 0)
	points, first, err := s.repo.RiskTrend(ctx, businessID, from)
	if err != nil {
		return out, fmt.Errorf("risk trend: %w", err)
	}

	// Never nil: an empty JSON array is easier for a chart to consume than null.
	if points == nil {
		points = []RiskTrendPoint{}
	}
	out.Points = points
	out.Meta.HistoryStartedAt = first
	out.Meta.MonthsAvailable = len(points)
	return out, nil
}

// segmentBounds are the customer-value buckets, in descending order. They are
// returned to the client rather than hard-coded in the frontend, so a change
// here does not need a frontend release.
var segmentBounds = []float64{1_000_000, 500_000, 100_000}

func (s *Service) CustomerSegments(ctx context.Context, businessID string) (CustomerSegments, error) {
	var out CustomerSegments

	settings, err := s.repo.GetBusinessSettings(ctx, businessID)
	if err != nil {
		return out, fmt.Errorf("load business settings: %w", err)
	}
	out.Currency = settings.Currency

	counts, err := s.repo.CustomerSegments(ctx, businessID, segmentBounds)
	if err != nil {
		return out, fmt.Errorf("customer segments: %w", err)
	}

	b := segmentBounds
	out.Segments = []Segment{
		{Label: "1M+", Min: b[0], Max: nil, CustomerCount: counts[0]},
		{Label: "500K–1M", Min: b[1], Max: &b[0], CustomerCount: counts[1]},
		{Label: "100K–500K", Min: b[2], Max: &b[1], CustomerCount: counts[2]},
		{Label: "<100K", Min: 0, Max: &b[2], CustomerCount: counts[3]},
	}
	return out, nil
}

// WriteRiskSnapshots is called by the daily job and once at startup.
func (s *Service) WriteRiskSnapshots(ctx context.Context) (int64, error) {
	return s.repo.WriteRiskSnapshots(ctx)
}

// --- helpers ---

// newMetric computes the period-over-period change.
//
// When the previous value is zero the change is left nil. A percentage change
// from zero is mathematically undefined, and reporting "+100%" would make every
// new business's first month look like explosive growth.
func newMetric(current, previous float64) Metric {
	m := Metric{Value: round2(current)}
	if previous == 0 {
		return m
	}
	change := round1((current - previous) * 100 / math.Abs(previous))
	m.Change = &change
	switch {
	case change > 0:
		m.Direction = "up"
	case change < 0:
		m.Direction = "down"
	default:
		m.Direction = "flat"
	}
	return m
}

func clampMonths(m int) int {
	if m <= 0 {
		return defaultMonths
	}
	if m > maxMonths {
		return maxMonths
	}
	return m
}

func clampLimit(l int) int {
	if l <= 0 {
		return defaultLimit
	}
	if l > maxLimit {
		return maxLimit
	}
	return l
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// monthLabel turns "2026-01" into "Jan", matching the chart's x-axis.
func monthLabel(yyyymm string) string {
	t, err := time.Parse("2006-01", yyyymm)
	if err != nil {
		return yyyymm
	}
	return t.Format("Jan")
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }
