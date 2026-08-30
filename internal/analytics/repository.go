package analytics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository holds every aggregation query.
//
// All money arithmetic happens here, in SQL over NUMERIC(14,2), where it is
// exact. No total is ever summed in Go — float64 appears only when the finished
// number is marshalled to JSON.
//
// Every query filters on business_id. There is no unscoped read in this file.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// BusinessSettings carries the currency and target that shape every response.
type BusinessSettings struct {
	Currency string
	Target   *float64
}

func (r *Repository) GetBusinessSettings(ctx context.Context, businessID string) (BusinessSettings, error) {
	const q = `SELECT currency, monthly_collection_target FROM businesses WHERE id = $1`
	var s BusinessSettings
	err := r.db.QueryRow(ctx, q, businessID).Scan(&s.Currency, &s.Target)
	return s, err
}

// OutstandingAt returns the net amount owed as at the end of the given date.
//
// A debt counts while it was live: issued on or before the date, and not yet
// settled as at that date. GREATEST(..., 0) stops an overpayment on one debt
// from cancelling out what is genuinely owed on another.
func (r *Repository) OutstandingAt(ctx context.Context, businessID string, asAt time.Time) (float64, error) {
	const q = `
		SELECT COALESCE(SUM(GREATEST(
			d.amount - COALESCE((
				SELECT SUM(p.amount) FROM payments p
				WHERE p.debt_id = d.id
				  AND p.deleted_at IS NULL
				  AND p.paid_at::date <= $2
			), 0), 0)), 0)
		FROM debts d
		WHERE d.business_id = $1
		  AND d.deleted_at IS NULL
		  AND d.issued_date <= $2
		  -- Settled on or before the cutoff, administratively or by payment.
		  AND (d.paid_at IS NULL OR d.paid_at::date > $2)`
	var out float64
	err := r.db.QueryRow(ctx, q, businessID, asAt).Scan(&out)
	return out, err
}

// OverdueAt returns the overdue total plus how many customers it spans, and how
// many of those debts first fell overdue within the given month.
func (r *Repository) OverdueAt(ctx context.Context, businessID string, asAt time.Time, monthStart time.Time) (total float64, customers int, newlyOverdue int, err error) {
	const q = `
		WITH overdue AS (
			SELECT d.customer_id, d.due_date, GREATEST(
				d.amount - COALESCE((
					SELECT SUM(p.amount) FROM payments p
					WHERE p.debt_id = d.id AND p.deleted_at IS NULL
					  AND p.paid_at::date <= $2
				), 0), 0) AS remaining
			FROM debts d
			WHERE d.business_id = $1
			  AND d.deleted_at IS NULL
			  AND d.due_date < $2
			  AND (d.paid_at IS NULL OR d.paid_at::date > $2)
		)
		SELECT COALESCE(SUM(remaining), 0),
		       COUNT(DISTINCT customer_id),
		       COUNT(*) FILTER (WHERE due_date >= $3)
		FROM overdue`
	err = r.db.QueryRow(ctx, q, businessID, asAt, monthStart).Scan(&total, &customers, &newlyOverdue)
	return
}

// CustomerCountAt counts customers created on or before the date and not since
// deleted, so the month-over-month comparison is meaningful.
func (r *Repository) CustomerCountAt(ctx context.Context, businessID string, asAt time.Time) (int, error) {
	const q = `
		SELECT COUNT(*)
		FROM customers
		WHERE business_id = $1
		  AND deleted_at IS NULL
		  AND created_at::date <= $2`
	var n int
	err := r.db.QueryRow(ctx, q, businessID, asAt).Scan(&n)
	return n, err
}

func (r *Repository) CustomersCreatedBetween(ctx context.Context, businessID string, from, to time.Time) (int, error) {
	const q = `
		SELECT COUNT(*)
		FROM customers
		WHERE business_id = $1 AND deleted_at IS NULL
		  AND created_at >= $2 AND created_at < $3`
	var n int
	err := r.db.QueryRow(ctx, q, businessID, from, to).Scan(&n)
	return n, err
}

// CollectedBetween sums live payments in the half-open window [from, to).
func (r *Repository) CollectedBetween(ctx context.Context, businessID string, from, to time.Time) (float64, error) {
	const q = `
		SELECT COALESCE(SUM(amount), 0)
		FROM payments
		WHERE business_id = $1 AND deleted_at IS NULL
		  AND paid_at >= $2 AND paid_at < $3`
	var out float64
	err := r.db.QueryRow(ctx, q, businessID, from, to).Scan(&out)
	return out, err
}

func (r *Repository) RecentDebts(ctx context.Context, businessID string, limit int) ([]RecentDebt, error) {
	const q = `
		SELECT d.id, d.customer_id, c.name, d.amount, d.due_date, d.status,
		       CASE WHEN d.due_date < CURRENT_DATE AND d.status <> 'paid'
		            THEN (CURRENT_DATE - d.due_date) END AS days_overdue
		FROM debts d
		JOIN customers c ON c.id = d.customer_id
		WHERE d.business_id = $1 AND d.deleted_at IS NULL
		ORDER BY d.created_at DESC
		LIMIT $2`
	rows, err := r.db.Query(ctx, q, businessID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentDebt, 0, limit)
	for rows.Next() {
		var d RecentDebt
		var due time.Time
		if err := rows.Scan(&d.ID, &d.CustomerID, &d.CustomerName, &d.Amount, &due, &d.Status, &d.DaysOverdue); err != nil {
			return nil, err
		}
		d.DueDate = due.Format("2006-01-02")
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *Repository) RecentPayments(ctx context.Context, businessID string, limit int) ([]RecentPayment, error) {
	const q = `
		SELECT p.id, p.customer_id, c.name, p.amount, p.method, p.paid_at
		FROM payments p
		JOIN customers c ON c.id = p.customer_id
		WHERE p.business_id = $1 AND p.deleted_at IS NULL
		ORDER BY p.paid_at DESC
		LIMIT $2`
	rows, err := r.db.Query(ctx, q, businessID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentPayment, 0, limit)
	for rows.Next() {
		var p RecentPayment
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.CustomerName, &p.Amount, &p.Method, &p.PaidAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RiskDistribution is computed live from customers: "now" is always accurate,
// so there is no reason to read it from the snapshot table.
func (r *Repository) RiskDistribution(ctx context.Context, businessID string) ([]RiskBucket, error) {
	const q = `
		SELECT risk_level, COUNT(*)
		FROM customers
		WHERE business_id = $1 AND deleted_at IS NULL
		GROUP BY risk_level`
	rows, err := r.db.Query(ctx, q, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{"low": 0, "medium": 0, "high": 0}
	total := 0
	for rows.Next() {
		var level string
		var n int
		if err := rows.Scan(&level, &n); err != nil {
			return nil, err
		}
		counts[level] = n
		total += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Always return all three buckets, in ascending risk order, so the chart
	// keeps a stable shape even when a level has no customers.
	out := make([]RiskBucket, 0, 3)
	for _, level := range []string{"low", "medium", "high"} {
		b := RiskBucket{RiskLevel: level, CustomerCount: counts[level]}
		if total > 0 {
			b.Percentage = round1(float64(counts[level]) * 100 / float64(total))
		}
		out = append(out, b)
	}
	return out, nil
}

// MonthlyCollections returns collections per month for the window, with a row
// for every month via generate_series — months with no payments must appear as
// zero rather than vanish and distort the chart.
func (r *Repository) MonthlyCollections(ctx context.Context, businessID string, from time.Time, months int) (map[string]float64, error) {
	const q = `
		WITH series AS (
			SELECT generate_series($2::date, $2::date + make_interval(months => $3 - 1), '1 month')::date AS m
		)
		SELECT to_char(s.m, 'YYYY-MM'),
		       COALESCE((
		           SELECT SUM(p.amount) FROM payments p
		           WHERE p.business_id = $1 AND p.deleted_at IS NULL
		             AND p.paid_at >= s.m
		             AND p.paid_at < s.m + interval '1 month'
		       ), 0)
		FROM series s
		ORDER BY s.m`
	rows, err := r.db.Query(ctx, q, businessID, from, months)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64, months)
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// RiskTrend reads the snapshot table, taking the latest snapshot within each
// month via DISTINCT ON. It also reports the earliest snapshot date so callers
// can tell a short series (young history) from an empty business.
func (r *Repository) RiskTrend(ctx context.Context, businessID string, from time.Time) ([]RiskTrendPoint, *string, error) {
	const q = `
		SELECT to_char(snapshot_date, 'YYYY-MM') AS month, low_count, medium_count, high_count
		FROM (
			SELECT DISTINCT ON (date_trunc('month', snapshot_date))
			       snapshot_date, low_count, medium_count, high_count
			FROM customer_risk_snapshots
			WHERE business_id = $1 AND snapshot_date >= $2
			ORDER BY date_trunc('month', snapshot_date), snapshot_date DESC
		) latest
		ORDER BY snapshot_date`
	rows, err := r.db.Query(ctx, q, businessID, from)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var points []RiskTrendPoint
	for rows.Next() {
		var p RiskTrendPoint
		if err := rows.Scan(&p.Month, &p.Low, &p.Medium, &p.High); err != nil {
			return nil, nil, err
		}
		p.Label = monthLabel(p.Month)
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	const firstQ = `
		SELECT to_char(MIN(snapshot_date), 'YYYY-MM-DD')
		FROM customer_risk_snapshots WHERE business_id = $1`
	var first *string
	if err := r.db.QueryRow(ctx, firstQ, businessID).Scan(&first); err != nil {
		return nil, nil, err
	}
	return points, first, nil
}

// CustomerSegments buckets customers by their lifetime debt total. Customers
// with no debts fall into the lowest bucket, which is why the join is a LEFT one.
func (r *Repository) CustomerSegments(ctx context.Context, businessID string, bounds []float64) ([]int, error) {
	const q = `
		WITH totals AS (
			SELECT c.id, COALESCE(SUM(d.amount), 0) AS lifetime
			FROM customers c
			LEFT JOIN debts d
			       ON d.customer_id = c.id AND d.deleted_at IS NULL
			WHERE c.business_id = $1 AND c.deleted_at IS NULL
			GROUP BY c.id
		)
		SELECT COUNT(*) FILTER (WHERE lifetime >= $2),
		       COUNT(*) FILTER (WHERE lifetime >= $3 AND lifetime < $2),
		       COUNT(*) FILTER (WHERE lifetime >= $4 AND lifetime < $3),
		       COUNT(*) FILTER (WHERE lifetime <  $4)
		FROM totals`
	out := make([]int, 4)
	err := r.db.QueryRow(ctx, q, businessID, bounds[0], bounds[1], bounds[2]).
		Scan(&out[0], &out[1], &out[2], &out[3])
	return out, err
}

// WriteRiskSnapshots records today's distribution for every tenant in one
// statement. ON CONFLICT makes it idempotent: a second run the same day
// corrects the row rather than duplicating it.
func (r *Repository) WriteRiskSnapshots(ctx context.Context) (int64, error) {
	const q = `
		INSERT INTO customer_risk_snapshots
			(business_id, snapshot_date, low_count, medium_count, high_count)
		SELECT business_id, CURRENT_DATE,
		       COUNT(*) FILTER (WHERE risk_level = 'low'),
		       COUNT(*) FILTER (WHERE risk_level = 'medium'),
		       COUNT(*) FILTER (WHERE risk_level = 'high')
		FROM customers
		WHERE deleted_at IS NULL
		GROUP BY business_id
		ON CONFLICT (business_id, snapshot_date) DO UPDATE
		SET low_count    = EXCLUDED.low_count,
		    medium_count = EXCLUDED.medium_count,
		    high_count   = EXCLUDED.high_count`
	tag, err := r.db.Exec(ctx, q)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
