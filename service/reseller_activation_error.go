package service

import "github.com/QuantumNous/new-api/common"

type ResellerActivationDeliveryError struct {
	Kind common.EmailDeliveryFailureKind
}

func (e *ResellerActivationDeliveryError) Error() string { return ErrEmailBindingDelivery.Error() }
func (e *ResellerActivationDeliveryError) Unwrap() error { return ErrEmailBindingDelivery }
