// Package audit records who performed the destructive and financial actions
// this API exposes, and serves that trail back.
//
// The README states that deleting a customer needs admin and voiding a payment
// needs owner. Enforcing that without recording it answers "could they?" but
// never "did they?" — which is the question asked after money goes missing.
package audit

import (
	"context"
	"log"
	"net/http"

	"github.com/Justdan111/credflow-api/internal/auth"
	appmiddleware "github.com/Justdan111/credflow-api/internal/middleware"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

type Service struct {
	repo *Repository
	// Resolving the actor's email and name needs a users lookup. The interface
	// is declared here, in the consumer, so this package does not import a
	// feature package and create a cycle.
	actors ActorLookup
}

// ActorLookup resolves the identity denormalised onto each entry.
type ActorLookup interface {
	ActorByID(ctx context.Context, userID string) (email, name string, err error)
}

func NewService(repo *Repository, actors ActorLookup) *Service {
	return &Service{repo: repo, actors: actors}
}

// Record writes one entry for the caller identified by the request.
//
// LIMITATION: the entry is written after the action has already committed, and
// a failure here is logged rather than returned. The alternative — failing the
// response — would tell the caller their delete did not happen when it did,
// which is a worse lie than a missing log line. A strictly transactional trail
// means threading the transaction through every mutating service; worth doing
// when a compliance requirement demands it, noted here rather than pretended
// away.
func (s *Service) Record(r *http.Request, action, entityType string, entityID *string, metadata map[string]any) {
	ctx := r.Context()

	businessID, ok := auth.BusinessIDFromContext(ctx)
	if !ok {
		return
	}
	userID, _ := auth.UserIDFromContext(ctx)

	email, name := "unknown", "unknown"
	var actorID *string
	if userID != "" {
		actorID = &userID
		if e, n, err := s.actors.ActorByID(ctx, userID); err == nil {
			email, name = e, n
		}
	}

	ip := appmiddleware.ClientIP(r)

	entry := Entry{
		BusinessID: businessID,
		ActorID:    actorID,
		ActorEmail: email,
		ActorName:  name,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		Metadata:   metadata,
		IP:         &ip,
	}

	// The request context is cancelled the moment the response is written, and
	// this runs at the very end of a handler. Using it would routinely abort
	// the insert, so the write gets its own background context.
	if err := s.repo.Insert(context.WithoutCancel(ctx), entry); err != nil {
		log.Printf("audit: record %s for business %s failed: %v", action, businessID, err)
	}
}

func (s *Service) List(ctx context.Context, businessID string, q ListQuery) ([]Entry, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = defaultPageSize
	}
	if q.PageSize > maxPageSize {
		q.PageSize = maxPageSize
	}
	return s.repo.List(ctx, businessID, q)
}
