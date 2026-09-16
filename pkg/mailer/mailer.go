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
	// SendInvitation carries the same kind of link as a password reset, but the
	// recipient has no account yet and no idea why a "reset" mail arrived. The
	// wording is the whole difference, so it is a separate method rather than a
	// reused one.
	SendInvitation(ctx context.Context, to, name, inviterName, setupURL string) error
}

// ConsoleMailer writes the message to the application log instead of sending
// it. Fine for development, and it makes the reset flow fully exercisable
// locally without any external service.
type ConsoleMailer struct{}

func NewConsoleMailer() *ConsoleMailer { return &ConsoleMailer{} }

func (m *ConsoleMailer) SendInvitation(_ context.Context, to, name, inviterName, setupURL string) error {
	// Same caveat as below: a production mailer must never log this URL.
	log.Printf("[mail] %s invited %s (%s) to CredFlow\n  set-password link: %s",
		inviterName, name, to, setupURL)
	return nil
}

func (m *ConsoleMailer) SendPasswordReset(_ context.Context, to, name, resetURL string) error {
	// The link is logged deliberately: in development the operator IS the
	// recipient. A production mailer must never log it — the URL is a
	// single-use credential.
	log.Printf("[mail] password reset for %s (%s)\n  reset link: %s", to, name, resetURL)
	return nil
}
