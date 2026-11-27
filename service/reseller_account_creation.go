package service

import "github.com/QuantumNous/new-api/model"

type ResellerAccountCreation struct {
	*model.ResellerIdentity
	MailError error `json:"-"`
}
