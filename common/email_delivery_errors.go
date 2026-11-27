package common

import (
	"errors"
	"net/textproto"
)

var ErrEmailDeliveryNotConfigured = NewMessage("SMTP server is not configured")

type EmailDeliveryFailureKind string

const (
	EmailDeliveryNotConfigured  EmailDeliveryFailureKind = "not_configured"
	EmailDeliveryConfiguration  EmailDeliveryFailureKind = "configuration"
	EmailDeliveryConnection     EmailDeliveryFailureKind = "connection"
	EmailDeliveryAuthentication EmailDeliveryFailureKind = "authentication"
	EmailDeliveryRejected       EmailDeliveryFailureKind = "rejected"
)

type emailDeliveryFailure struct {
	kind EmailDeliveryFailureKind
	err  error
}

func (e emailDeliveryFailure) Error() string { return e.err.Error() }
func (e emailDeliveryFailure) Unwrap() error { return e.err }

func ClassifyEmailDeliveryFailure(err error) EmailDeliveryFailureKind {
	if errors.Is(err, ErrEmailDeliveryNotConfigured) {
		return EmailDeliveryNotConfigured
	}
	if errors.Is(err, ErrEmailDeliveryUnavailable) {
		return EmailDeliveryConfiguration
	}
	var failure emailDeliveryFailure
	if errors.As(err, &failure) {
		return failure.kind
	}
	return ""
}

func classifySMTPSubmissionFailure(err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return emailDeliveryFailure{EmailDeliveryRejected, err}
	}
	return err
}

func configuredEmailFailureKind(stage string, err error) EmailDeliveryFailureKind {
	switch stage {
	case "connect_tls":
		return EmailDeliveryConnection
	case "auth":
		return EmailDeliveryAuthentication
	case "submit":
		return ClassifyEmailDeliveryFailure(classifySMTPSubmissionFailure(err))
	}
	return ClassifyEmailDeliveryFailure(err)
}
