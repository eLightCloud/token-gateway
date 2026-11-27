package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
)

type walletSettlementObservation struct {
	requestBalance int64
	spent          int64
}

// The user quota batch lock protects observations and the wallet entries they follow.
var walletSettlementObservations = make(map[int]walletSettlementObservation)

func ObserveLegacyWalletSettlement(userID int, requestBalance, spent int64) {
	if !common.QuotaRemindEnabled || userID <= 0 || spent <= 0 {
		return
	}
	if common.BatchUpdateEnabled {
		lock := &batchUpdateLocks[BatchUpdateTypeUserQuota]
		lock.Lock()
		prior := walletSettlementObservations[userID]
		walletSettlementObservations[userID] = walletSettlementObservation{
			requestBalance: max(prior.requestBalance, requestBalance),
			spent:          prior.spent + min(spent, common.MaxWalletQuota-prior.spent),
		}
		lock.Unlock()
		return
	}
	observeCommittedLegacyWallet(userID, walletSettlementObservation{requestBalance: requestBalance, spent: spent})
}

func observeCommittedLegacyWallet(userID int, observation walletSettlementObservation) {
	if err := ObserveWalletSpend(userID, observation.requestBalance, observation.spent, time.Now()); err != nil {
		common.SysError(common.LogText("failed to observe wallet reminder for user %d: %s", userID, err.Error()))
	}
}
