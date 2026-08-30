// Package mailer delivers transactional email.
//
// The interface exists so password recovery could ship without first choosing
// an email vendor: a security fix should not wait on a procurement decision.
// ConsoleMailer is the development default; a real provider is one new file
// implementing the same two methods, swapped in at wiring time.
package mailer

import (
	"context"
	"log"
)

type Mailer interface {
	SendPasswordReset(ctx context.Context, to, name, resetURL string) error
}

// ConsoleMailer writes the message to the application log instead of sending
// it. Fine for development, and it makes the reset flow fully exercisable
// locally without any external service.
type ConsoleMailer struct{}

func NewConsoleMailer() *ConsoleMailer { return &ConsoleMailer{} }

func (m *ConsoleMailer) SendPasswordReset(_ context.Context, to, name, resetURL string) error {
	// The link is logged deliberately: in development the operator IS the
	// recipient. A production mailer must never log it — the URL is a
	// single-use credential.
	log.Printf("[mail] password reset for %s (%s)\n  reset link: %s", to, name, resetURL)
	return nil
}
