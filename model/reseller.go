package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrResellerForbidden = errors.New("reseller operation forbidden")
	ErrResellerInvalid   = errors.New("invalid reseller input")
	ErrResellerConflict  = errors.New("reseller operation conflict")
)

// ResellerIdentity has no update/delete API. Absence means an ordinary user.
// A single primary key makes agent/customer identities mutually exclusive.
type ResellerIdentity struct {
	ActivationMailSent bool   `gorm:"-" json:"activation_mail_sent"`
	UserID             int    `gorm:"primaryKey;autoIncrement:false" json:"user_id"`
	Kind               string `gorm:"size:20;not null" json:"kind"`
	AgentUserID        int    `gorm:"index;not null" json:"agent_user_id"`
	Activation         string `gorm:"size:20;not null" json:"activation"`
	AgentDisabled      bool   `json:"agent_disabled"`
	CreatedAt          int64  `json:"created_at"`
}

type ResellerAccount struct {
	UserID      int   `gorm:"primaryKey;autoIncrement:false" json:"user_id"`
	Enabled     bool  `json:"enabled"`
	Balance     int64 `gorm:"type:bigint;not null" json:"balance"`
	Reserved    int64 `gorm:"type:bigint;not null" json:"reserved"`
	Revision    int64 `json:"revision"`
	PublishedAt int64 `json:"published_at"`
}

type ResellerQuotaAdjustment struct {
	ID           int64  `gorm:"primaryKey" json:"id"`
	OperationID  string `gorm:"size:64;not null" json:"operation_id"`
	AccountKind  string `gorm:"size:20;not null" json:"account_kind"`
	UserID       int    `gorm:"index;not null" json:"user_id"`
	OperatorID   int    `json:"operator_id"`
	Reason       string `gorm:"type:text;not null" json:"reason"`
	Mode         string `gorm:"size:10;not null" json:"mode"`
	Quota        int64  `gorm:"type:bigint;not null" json:"quota"`
	Before       int64  `gorm:"type:bigint;not null" json:"before"`
	After        int64  `gorm:"type:bigint;not null" json:"after"`
	OccurredAt   int64  `json:"occurred_at"`
	OperationKey string `gorm:"size:64;uniqueIndex;not null" json:"-"`
}

// Digest keys keep composite indexes within MySQL 5.7 utf8mb4 limits.
// Readers must also compare the full original model name before using a row.
type ResellerModelConfig struct {
	ID         int64  `gorm:"primaryKey"`
	UserID     int    `gorm:"uniqueIndex:reseller_config_key"`
	ModelKey   string `gorm:"size:64;uniqueIndex:reseller_config_key"`
	Model      string `gorm:"type:text;not null"`
	ChannelID  int    `gorm:"uniqueIndex:reseller_config_key"`
	Authorized bool
	Ratio      int64 `gorm:"not null"` // millionths, 1..2_000_000
	Revision   int64
}

type ResellerConfigRevision struct {
	ID         int64 `gorm:"primaryKey"`
	UserID     int   `gorm:"uniqueIndex:reseller_revision"`
	Revision   int64 `gorm:"uniqueIndex:reseller_revision"`
	OperatorID int
	Snapshot   string `gorm:"type:text;not null"`
	CreatedAt  int64
}

type ResellerRequest struct {
	EntityType          string `gorm:"size:32;index:reseller_request_entity,priority:1"`
	EntityID            int64  `gorm:"index:reseller_request_entity,priority:2"`
	ID                  string `gorm:"size:64;primaryKey"`
	TaskID              string `gorm:"size:64;index"`
	AgentUserID         int    `gorm:"index"`
	CustomerUserID      int    `gorm:"index"`
	Model               string `gorm:"type:text"`
	ChannelID           int
	Snapshot            string `gorm:"type:text"`
	State               string `gorm:"size:32;index"` // reserved, outcome_unknown, pending, settled, refunded
	Version             int64
	BaseQuota           int64  `gorm:"type:bigint"`
	ReservedQuota       int64  `gorm:"type:bigint"`
	CustomerQuota       *int64 `gorm:"type:bigint"`
	AgentQuota          *int64 `gorm:"type:bigint"`
	OriginalOperationID string `gorm:"size:64"`
	CreatedAt           int64
	OccurredAt          int64
}

type ResellerEvent struct {
	ID                  int64  `gorm:"primaryKey"`
	OperationID         string `gorm:"size:64;uniqueIndex;not null"`
	RequestID           string `gorm:"size:64;uniqueIndex:reseller_event_version"`
	Version             int64  `gorm:"uniqueIndex:reseller_event_version"`
	Kind                string `gorm:"size:10"`
	AgentUserID         int    `gorm:"index:reseller_event_pending,priority:2"`
	CustomerUserID      int
	Model               string `gorm:"type:text"`
	ChannelID           int
	Snapshot            string `gorm:"type:text"`
	BaseQuota           int64  `gorm:"type:bigint"`
	CustomerTarget      int64  `gorm:"type:bigint"`
	CustomerDelta       int64  `gorm:"type:bigint"`
	AgentTarget         int64  `gorm:"type:bigint"`
	ReserveDelta        int64  `gorm:"type:bigint"`
	SpendDelta          int64  `gorm:"type:bigint"`
	OriginalOperationID string `gorm:"size:64"`
	OccurredAt          int64
	ProcessedRevision   int64
	ProcessedAt         int64 `gorm:"index:reseller_event_pending,priority:1"`
}

func migrateReseller(db *gorm.DB) error {
	return db.AutoMigrate(&UserSecurityState{}, &SecurityRecoveryRecord{}, &ResellerIdentity{}, &ResellerAccount{}, &ResellerQuotaAdjustment{}, &ResellerConfigHead{}, &ResellerModelConfig{}, &ResellerConfigRevision{}, &ResellerFundingSnapshot{}, &ResellerBillingReceipt{}, &ResellerProjection{}, &ResellerRequest{}, &ResellerEvent{})
}

func GetResellerIdentity(tx *gorm.DB, userID int) (*ResellerIdentity, error) {
	var identity ResellerIdentity
	err := tx.First(&identity, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDatabase, err)
	}
	return &identity, nil
}

// RequireOrdinaryUser is a domain guard, including for root/admin callers.
func RequireOrdinaryUser(tx *gorm.DB, userID int) error {
	kind, err := ResellerUserKind(tx, userID)
	if err != nil {
		return err
	}
	if kind != "" {
		return ErrResellerForbidden
	}
	return nil
}

type ResellerCreateInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

// CreateResellerUser creates only new accounts. Credentials and activation are
// owned by S2; no initial password, invitation reward or signup quota is issued.
func CreateResellerUser(operatorID int, kind string, input ResellerCreateInput) (*ResellerIdentity, error) {
	input.Username = strings.TrimSpace(input.Username)
	input.Email = NormalizeEmail(input.Email)
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 50 || input.Username == "" || utf8.RuneCountInString(input.Username) > UserNameMaxLength || utf8.RuneCountInString(input.DisplayName) > 20 || (kind != "agent" && kind != "agent_customer") {
		return nil, ErrResellerInvalid
	}
	var identity ResellerIdentity
	err = DB.Transaction(func(tx *gorm.DB) error {
		var operator User
		if err := lockForUpdate(tx).First(&operator, operatorID).Error; err != nil {
			return err
		}
		if operator.Status != common.UserStatusEnabled {
			return ErrResellerForbidden
		}
		parent, err := GetResellerIdentity(tx, operatorID)
		if err != nil {
			return err
		}
		agentID := 0
		if kind == "agent" {
			if operator.Role < common.RoleAdminUser || parent != nil {
				return ErrResellerForbidden
			}
		} else {
			if parent == nil || parent.Kind != "agent" || parent.Activation != "active" {
				return ErrResellerForbidden
			}
			var account ResellerAccount
			if err := tx.First(&account, "user_id = ?", operatorID).Error; err != nil {
				return err
			}
			if !account.Enabled {
				return ErrResellerForbidden
			}
			agentID = operatorID
		}
		return withNormalizedEmailLock(tx, input.Email, func(tx *gorm.DB) error {
			if err := ensureEmailAvailableWithTx(tx, input.Email, 0); err != nil {
				return err
			}
			user := User{ResellerKind: kind, AffCode: common.GetRandomString(24), Username: input.Username, DisplayName: input.DisplayName, Email: input.Email, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			identity = ResellerIdentity{UserID: user.Id, Kind: kind, AgentUserID: agentID, Activation: "pending", CreatedAt: common.GetTimestamp()}
			if err := tx.Create(&identity).Error; err != nil {
				return err
			}
			if kind == "agent" {
				return tx.Create(&ResellerAccount{UserID: user.Id, Enabled: true}).Error
			}
			return nil
		})
	})
	return &identity, resellerWriteError(err)
}

type ResellerQuotaInput struct {
	OperationID string `json:"operation_id"`
	Mode        string `json:"mode"`
	Quota       int64  `json:"quota"`
	Reason      string `json:"reason"`
}

func AdjustResellerQuota(operatorID, userID int, input ResellerQuotaInput) (*ResellerQuotaAdjustment, error) {
	return adjustResellerQuota(operatorID, userID, "", input)
}

func AdjustResellerQuotaForKind(operatorID, userID int, kind string, input ResellerQuotaInput) (*ResellerQuotaAdjustment, error) {
	return adjustResellerQuota(operatorID, userID, kind, input)
}

func adjustResellerQuota(operatorID, userID int, kind string, input ResellerQuotaInput) (*ResellerQuotaAdjustment, error) {
	if len(input.OperationID) == 0 || len(input.OperationID) > 64 || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 1000 || input.Quota <= 0 || input.Quota > common.MaxWalletQuota || (input.Mode != "add" && input.Mode != "subtract") {
		return nil, ErrResellerInvalid
	}
	// Restrict operation IDs to ASCII so MySQL byte lengths and equality agree.
	for _, r := range input.OperationID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':') {
			return nil, ErrResellerInvalid
		}
	}
	operationKey := fmt.Sprintf("%x", sha256.Sum256([]byte(input.OperationID)))
	var receipt ResellerQuotaAdjustment
	err := DB.Transaction(func(tx *gorm.DB) error {
		var operator User
		if err := tx.First(&operator, operatorID).Error; err != nil {
			return err
		}
		if operator.Status != common.UserStatusEnabled {
			return ErrResellerForbidden
		}
		target, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if target == nil || kind != "" && target.Kind != kind {
			return gorm.ErrRecordNotFound
		}
		parent, err := GetResellerIdentity(tx, operatorID)
		if err != nil {
			return err
		}
		if target.Kind == "agent" {
			if parent != nil || operator.Role < common.RoleAdminUser {
				return ErrResellerForbidden
			}
		} else {
			if parent == nil || parent.Kind != "agent" || parent.Activation != "active" || target.AgentUserID != operatorID {
				return gorm.ErrRecordNotFound
			}
			var account ResellerAccount
			if err := tx.First(&account, "user_id = ?", operatorID).Error; err != nil {
				return err
			}
			if !account.Enabled {
				return ErrResellerForbidden
			}
		}
		var before, reserved int64
		if target.Kind == "agent" {
			var account ResellerAccount
			if err := lockForUpdate(tx).First(&account, "user_id = ?", userID).Error; err != nil {
				return err
			}
			before, reserved = account.Balance, account.Reserved
		} else {
			var user User
			if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
				return err
			}
			before = user.Quota
		}
		err = lockForUpdate(tx).Where("operation_key = ?", operationKey).First(&receipt).Error
		if err == nil {
			if receipt.OperationID != input.OperationID || receipt.UserID != userID || receipt.OperatorID != operatorID || receipt.Mode != input.Mode || receipt.Quota != input.Quota || receipt.Reason != input.Reason {
				return ErrResellerOperationConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		delta := input.Quota
		if input.Mode == "subtract" {
			delta = -delta
		}
		if before > common.MaxWalletQuota || (target.Kind != "agent" && before < common.MinWalletQuota) || reserved < 0 || (delta > 0 && before > common.MaxWalletQuota-delta) || (delta < 0 && (before < input.Quota || before-input.Quota < reserved)) {
			return ErrResellerBalanceConflict
		}
		after := before + delta
		if target.Kind == "agent" {
			result := tx.Model(&ResellerAccount{}).Where("user_id = ? AND balance = ?", userID, before).Updates(map[string]any{"balance": after, "revision": gorm.Expr("revision + 1"), "published_at": common.GetTimestamp()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrResellerBalanceConflict
			}
		} else {
			result := tx.Model(&User{}).Where("id = ? AND quota = ?", userID, before).Update("quota", after)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrResellerBalanceConflict
			}
		}
		receipt = ResellerQuotaAdjustment{OperationKey: operationKey, OperationID: input.OperationID, AccountKind: target.Kind, UserID: userID, OperatorID: operatorID, Mode: input.Mode, Quota: input.Quota, Reason: input.Reason, Before: before, After: after, OccurredAt: common.GetTimestamp()}
		return tx.Create(&receipt).Error
	})
	if err != nil {
		return nil, resellerWriteError(err)
	}
	if receipt.AccountKind == "agent_customer" {
		if err := invalidateUserCache(userID); err != nil {
			common.SysError("reseller quota cache refresh failed: " + err.Error())
		}
	}
	return &receipt, nil
}

// Database uniqueness is a public conflict, never a raw SQL error or an
// apparent persistence outage. Keep this mapping local to reseller writes.
func resellerWriteError(err error) error {
	if err == nil {
		return nil
	}
	var mysqlError *mysqlDriver.MySQLError
	var postgresError *pgconn.PgError
	var sqliteError interface{ Code() int }
	if errors.Is(err, gorm.ErrDuplicatedKey) || (errors.As(err, &mysqlError) && mysqlError.Number == 1062) || (errors.As(err, &postgresError) && postgresError.Code == "23505") || (errors.As(err, &sqliteError) && (sqliteError.Code() == 2067 || sqliteError.Code() == 1555)) {
		return ErrResellerDuplicateResource
	}
	if errors.Is(err, ErrResellerConflict) || errors.Is(err, ErrResellerInvalid) || errors.Is(err, ErrResellerForbidden) || errors.Is(err, ErrEmailAlreadyTaken) || errors.Is(err, ErrResellerDataCorrupt) || errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// All remaining failures here come from a database write or its transaction.
	return fmt.Errorf("%w: %w", ErrDatabase, err)
}
