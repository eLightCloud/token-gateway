package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RelayOperation is the durable ownership record for a client-supplied
// idempotency key. An unresolved claim never authorizes another upstream call.
type RelayOperation struct {
	KeyHash     string `gorm:"primaryKey;size:64"`
	RequestHash string `gorm:"size:64;not null"`
	RequestID   string `gorm:"size:64;not null"`
	Result      []byte
	CompletedAt *int64
	CreatedAt   int64
}

type RelayOperationResult struct {
	Status          int    `json:"status"`
	ContentType     string `json:"content_type"`
	ContentEncoding string `json:"content_encoding,omitempty"`
	Body            []byte `json:"body"`
}

var errRelayOperationNotPending = errors.New("relay operation is no longer pending")

func ClaimRelayOperation(claim RelayOperation) (RelayOperation, error) {
	err := DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key_hash"}}, DoNothing: true}).Create(&claim).Error
	if err != nil {
		return RelayOperation{}, err
	}
	var stored RelayOperation
	err = DB.First(&stored, "key_hash = ?", claim.KeyHash).Error
	return stored, err
}

func (operation RelayOperation) Replay() (RelayOperationResult, error) {
	var result RelayOperationResult
	sealer, err := relayOperationCipher()
	if err != nil {
		return result, err
	}
	plain, err := sealer.Open(nil, nil, operation.Result, []byte(operation.KeyHash))
	if err != nil {
		return result, err
	}
	err = common.Unmarshal(plain, &result)
	return result, err
}

func relayOperationCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("relay-operation-response\x00" + common.CryptoSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// A nil result marks a finished operation without retaining a replay body.
func saveRelayOperationResult(tx *gorm.DB, operation RelayOperation, result *RelayOperationResult) error {
	var encoded []byte
	if result != nil {
		sealer, err := relayOperationCipher()
		if err != nil {
			return err
		}
		plain, err := common.Marshal(result)
		if err != nil {
			return err
		}
		encoded = sealer.Seal(nil, nil, plain, []byte(operation.KeyHash))
	}
	update := tx.Model(&RelayOperation{}).Where("key_hash = ? AND request_id = ? AND completed_at IS NULL",
		operation.KeyHash, operation.RequestID).Updates(map[string]any{"result": encoded, "completed_at": common.GetTimestamp()})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errRelayOperationNotPending
	}
	return nil
}

func FailRelayOperation(operation RelayOperation, result *RelayOperationResult) error {
	return saveRelayOperationResult(DB, operation, result)
}

// EnsureRelaySettlement publishes a replayable success only in the same
// transaction that makes its settlement recoverable. A non-nil journal with
// an error indicates an uncertain commit outcome, so a refund is unsafe.
func EnsureRelaySettlement(journal *TaskSettlementJournal, operation RelayOperation, result *RelayOperationResult) (*TaskSettlementJournal, error) {
	if journal.RequestId != "settle:"+operation.RequestID {
		return nil, fmt.Errorf("settlement does not belong to relay operation")
	}
	for {
		var stored *TaskSettlementJournal
		var retryWithoutBody bool
		err := DB.Transaction(func(tx *gorm.DB) error {
			settlement, err := ensureTaskSettlementJournalTx(tx, journal)
			if err != nil {
				return err
			}
			if err := saveRelayOperationResult(tx, operation, result); err != nil {
				retryWithoutBody = result != nil && !errors.Is(err, errRelayOperationNotPending)
				return err
			}
			stored = settlement
			return nil
		})
		if !retryWithoutBody {
			return stored, err
		}
		// The callback failed before COMMIT. Retry in a new transaction in
		// case the database rejected the optional body or closed the connection.
		result = nil
	}
}
