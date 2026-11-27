package model

import "errors"

type ResellerCreateFieldError struct {
	Field   string
	Message string
}

func (e *ResellerCreateFieldError) Error() string { return e.Message }
func (e *ResellerCreateFieldError) Unwrap() error { return ErrResellerInvalid }

type ResellerOperatorStateError struct {
	Code    string
	Message string
}

func (e *ResellerOperatorStateError) Error() string { return e.Message }
func (e *ResellerOperatorStateError) Unwrap() error { return ErrResellerForbidden }

var (
	ErrResellerAgentNotActivated = &ResellerOperatorStateError{
		Code: "RESELLER_AGENT_NOT_ACTIVATED", Message: "Activate your reseller account before managing customers.",
	}
	ErrResellerOperatorDisabled = &ResellerOperatorStateError{
		Code: "RESELLER_OPERATOR_DISABLED", Message: "Your account is disabled. Contact the platform administrator.",
	}
	ErrResellerActivationUnavailable    = errors.New("this account cannot receive an activation link in its current state")
	ErrResellerActivationAddressChanged = errors.New("the activation email address changed")
	ErrResellerActivationTargetDisabled = errors.New("the activation target is disabled")
	ErrResellerActivationPasswordSet    = errors.New("the activation target already has a password")
)
