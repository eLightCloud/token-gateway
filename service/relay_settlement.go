package service

import (
	"context"

	"github.com/QuantumNous/new-api/model"
)

type relaySettlementKey struct{}

// RelaySettlement exists only on HTTP paths that hold their success response
// until settlement is durable. The operation is optional for unkeyed calls.
type RelaySettlement struct {
	Operation *model.RelayOperation
	Result    func() *model.RelayOperationResult
	Commit    func() error
	Durable   bool
	Uncertain bool
}

func WithRelaySettlement(ctx context.Context, settlement *RelaySettlement) context.Context {
	return context.WithValue(ctx, relaySettlementKey{}, settlement)
}

func RelaySettlementFor(ctx context.Context) *RelaySettlement {
	if ctx == nil {
		return nil
	}
	settlement, _ := ctx.Value(relaySettlementKey{}).(*RelaySettlement)
	return settlement
}

func persistRelaySettlement(proof *RelaySettlement, journal *model.TaskSettlementJournal) (*model.TaskSettlementJournal, error) {
	var stored *model.TaskSettlementJournal
	var err error
	if proof.Operation == nil {
		stored, err = model.EnsureTaskSettlementJournal(journal)
	} else {
		stored, err = model.EnsureRelaySettlement(journal, *proof.Operation, proof.Result())
	}
	proof.Durable = err == nil
	proof.Uncertain = err != nil && stored != nil
	return stored, err
}
