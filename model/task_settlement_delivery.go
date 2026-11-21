package model

import "gorm.io/gorm"

// FinishRelaySettlementLog acknowledges a delivered log and removes its
// completed journal in one commit. Only call after the log store confirms
// delivery. Either both changes commit, or the journal remains recoverable.
func FinishRelaySettlementLog(requestID string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := markTaskSettlementLogDone(tx, requestID); err != nil {
			return err
		}
		return deleteTaskSettlementJournal(tx, requestID)
	})
}
