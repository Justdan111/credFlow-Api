package audit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is append-and-read only: no Update, no Delete. A trail the
// application can edit is not evidence of anything.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const entryColumns = `
	id, business_id, actor_id, actor_email, actor_name,
	action, entity_type, entity_id, metadata, ip, created_at
`

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(
		&e.ID, &e.BusinessID, &e.ActorID, &e.ActorEmail, &e.ActorName,
		&e.Action, &e.EntityType, &e.EntityID, &e.Metadata, &e.IP, &e.CreatedAt,
	)
	return e, err
}

func (r *Repository) Insert(ctx context.Context, e Entry) error {
	const q = `
		INSERT INTO audit_logs
			(business_id, actor_id, actor_email, actor_name,
			 action, entity_type, entity_id, metadata, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, '{}'::JSONB), $9)
	`
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	_, err := r.db.Exec(ctx, q,
		e.BusinessID, e.ActorID, e.ActorEmail, e.ActorName,
		e.Action, e.EntityType, e.EntityID, metadata, e.IP,
	)
	return err
}

func (r *Repository) List(ctx context.Context, businessID string, q ListQuery) ([]Entry, int, error) {
	// Every value is a bind parameter; no caller input reaches the SQL text.
	where := " WHERE business_id = $1"
	args := []any{businessID}

	add := func(clause string, value any) {
		args = append(args, value)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}
	if q.Action != "" {
		add("action", q.Action)
	}
	if q.EntityType != "" {
		add("entity_type", q.EntityType)
	}
	if q.EntityID != "" {
		add("entity_id", q.EntityID)
	}
	if q.ActorID != "" {
		add("actor_id", q.ActorID)
	}

	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit logs: %w", err)
	}

	// id breaks ties: entries sharing a timestamp would otherwise swap pages.
	listSQL := `SELECT ` + entryColumns + ` FROM audit_logs` + where +
		fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)

	rows, err := r.db.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, q.PageSize)
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan audit log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}
