package email

import "context"

type VerificationMessage struct{ To, DisplayName, URL, IdempotencyKey string }
type PasswordResetMessage struct{ To, DisplayName, URL, IdempotencyKey string }
type SecurityAlertMessage struct{ To, DisplayName, Event, IdempotencyKey string }

type Sender interface {
	SendVerificationEmail(context.Context, VerificationMessage) error
	SendPasswordResetEmail(context.Context, PasswordResetMessage) error
	SendSecurityAlert(context.Context, SecurityAlertMessage) error
}
