package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const VantageTokenQuotaContract = "native-token-quota-1"

var (
	ErrVantageQuotaOperationNotFound        = errors.New("quota operation not found")
	ErrVantageQuotaOperationConflict        = errors.New("quota operation conflicts with existing intent")
	ErrVantageQuotaInvalidState             = errors.New("native token quota is out of range")
	ErrVantageEnterpriseRotationUnsupported = errors.New("enterprise credential rotation is not supported")
)

// Apply before both new rotations and committed-operation recovery. Reporting
// not_ready after issuing an unlimited replacement Key cannot enforce policy.
func validateVantageCredentialRotation(credentialRef string) error {
	credential, err := GetVantageCredentialBinding(credentialRef)
	if err != nil {
		return err
	}
	account, err := GetVantageAccountBindingById(credential.AccountBindingId)
	if err != nil {
		return err
	}
	if account.AccountType == VantageAccountTypeEnterprise {
		return ErrVantageEnterpriseRotationUnsupported
	}
	return nil
}

// Only policy metadata lives here. Native Token fields remain the sole source
// of balances and usage; no employee identity or parallel accounting is stored.
type VantageTokenQuotaConfig struct {
	TokenId      int    `gorm:"primaryKey;autoIncrement:false"`
	Revision     int64  `gorm:"type:bigint"`
	PolicyDigest string `gorm:"type:varchar(64)"`
}

// Committed policy changes and their recoverable result share a transaction.
// Operations are retained without automatic expiry, including rejected intents.
type VantageTokenQuotaOperation struct {
	Id               int     `gorm:"primaryKey" json:"-"`
	Namespace        string  `gorm:"type:varchar(32);uniqueIndex:uk_vantage_quota_op,priority:1" json:"-"`
	OperationId      string  `gorm:"type:varchar(128);uniqueIndex:uk_vantage_quota_op,priority:2" json:"operation_id"`
	AccountBindingId int     `gorm:"index" json:"-"`
	TokenId          int     `json:"-"`
	TenantRef        string  `gorm:"type:varchar(128)" json:"tenant_ref"`
	CredentialRef    string  `gorm:"type:varchar(64)" json:"credential_ref"`
	ActorRef         string  `gorm:"type:varchar(128)" json:"actor_ref"`
	RequestDigest    string  `gorm:"type:varchar(64)" json:"request_digest"`
	ExpectedRevision int64   `gorm:"type:bigint" json:"expected_revision"`
	Currency         string  `gorm:"type:varchar(8)" json:"currency"`
	LimitAmount      string  `gorm:"type:varchar(32)" json:"limit_amount"`
	QuotaPerUnit     string  `gorm:"type:varchar(32)" json:"quota_per_unit"`
	Status           string  `gorm:"type:varchar(16)" json:"status"`
	AppliedRevision  *int64  `gorm:"type:bigint" json:"applied_revision"`
	ReasonCode       *string `gorm:"type:varchar(40)" json:"reason_code"`
	CreatedAt        int64   `gorm:"autoCreateTime" json:"created_at"`
}

type VantageTokenQuotaSnapshot struct {
	Account    *VantageAccountBinding
	Credential *VantageCredentialBinding
	Token      *Token
	User       *User
	Revision   int64
}

// Called only on the dedicated enterprise first-create path, before INSERT.
// Idempotent recovery never applies defaults to an existing Token.
func initializeVantageEnterpriseTokenQuota(token *Token) {
	token.UnlimitedQuota = false
	token.RemainQuota = 0
	token.UsedQuota = 0
}

func vantageNativeTokenTotal(token *Token) (int64, error) {
	if token.UsedQuota < 0 || token.RemainQuota > 0 && token.UsedQuota > math.MaxInt64-token.RemainQuota {
		return 0, ErrVantageQuotaInvalidState
	}
	total := token.UsedQuota + token.RemainQuota
	if total < 0 {
		return 0, ErrVantageQuotaInvalidState
	}
	return total, nil
}

// Callers lock native Tokens first. Only changed policies write configuration;
// an unchanged page needs one metadata query, regardless of its item count.
func vantageTokenQuotaPolicies(tx *gorm.DB, tokens []Token) (map[int]int64, error) {
	ids := make([]int, 0, len(tokens))
	for _, token := range tokens {
		ids = append(ids, token.Id)
	}
	var configs []VantageTokenQuotaConfig
	if len(ids) > 0 {
		if err := lockForUpdate(tx).Where("token_id IN ?", ids).Find(&configs).Error; err != nil {
			return nil, err
		}
	}
	byToken := make(map[int]VantageTokenQuotaConfig, len(configs))
	for _, config := range configs {
		byToken[config.TokenId] = config
	}
	revisions := make(map[int]int64, len(tokens))
	for _, token := range tokens {
		total, err := vantageNativeTokenTotal(&token)
		if err != nil {
			return nil, err
		}
		policy := fmt.Sprintf("limited:%d", total)
		if token.UnlimitedQuota {
			policy = "unlimited"
		}
		digest := sha256.Sum256([]byte(policy))
		encoded := hex.EncodeToString(digest[:])
		config, found := byToken[token.Id]
		if !found {
			config = VantageTokenQuotaConfig{TokenId: token.Id, Revision: 1, PolicyDigest: encoded}
			if err := tx.Create(&config).Error; err != nil {
				return nil, err
			}
		} else if config.PolicyDigest != encoded {
			if config.Revision >= common.MaxWalletQuota {
				return nil, ErrVantageQuotaInvalidState
			}
			config.Revision++
			config.PolicyDigest = encoded
			if err := tx.Save(&config).Error; err != nil {
				return nil, err
			}
		}
		revisions[token.Id] = config.Revision
	}
	return revisions, nil
}

// Account/namespace and native ownership checks are routing boundaries only.
// We do not decide Vantage administrator or membership business permissions.
func vantageQuotaAccount(tx *gorm.DB, tenant string) (*VantageAccountBinding, *User, error) {
	var account VantageAccountBinding
	err := tx.Session(&gorm.Session{}).Where("namespace = ? AND account_type = ? AND external_ref = ?", VantageIntegrationNamespace, VantageAccountTypeEnterprise, tenant).Take(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrVantageAccountNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var user User
	if err := tx.Session(&gorm.Session{}).First(&user, account.UserId).Error; err != nil {
		return nil, nil, err
	}
	return &account, &user, nil
}

func vantageQuotaToken(tx *gorm.DB, account *VantageAccountBinding, credentialRef string) (*VantageCredentialBinding, *Token, error) {
	var credential VantageCredentialBinding
	err := tx.Where("namespace = ? AND account_binding_id = ? AND credential_ref = ?", VantageIntegrationNamespace, account.Id, credentialRef).Take(&credential).Error
	if err != nil || credential.TokenId == nil {
		if err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrVantageCredentialNotFound
		}
		return nil, nil, err
	}
	var token Token
	err = lockForUpdate(tx.Unscoped()).Where("id = ? AND user_id = ?", *credential.TokenId, account.UserId).Take(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrVantageCredentialNotFound
	}
	return &credential, &token, err
}

func GetVantageTokenQuota(tenant, credentialRef string) (*VantageTokenQuotaSnapshot, error) {
	var snapshot *VantageTokenQuotaSnapshot
	err := DB.Transaction(func(tx *gorm.DB) error {
		account, user, err := vantageQuotaAccount(tx, tenant)
		if err != nil {
			return err
		}
		credential, token, err := vantageQuotaToken(tx, account, credentialRef)
		if err != nil {
			return err
		}
		revisions, err := vantageTokenQuotaPolicies(tx, []Token{*token})
		if err != nil {
			return err
		}
		snapshot = &VantageTokenQuotaSnapshot{Account: account, User: user, Credential: credential, Token: token, Revision: revisions[token.Id]}
		return nil
	})
	return snapshot, err
}

func GetVantageQuotaFunds(tenant string) (*VantageAccountBinding, *User, error) {
	return vantageQuotaAccount(DB, tenant)
}

// Usage filters resolve attribution only; they must not create policy revisions
// or lock a live token while reading historical usage (including revoked keys).
func GetVantageQuotaUsageTokenID(account *VantageAccountBinding, credentialRef string) (int, error) {
	var credential VantageCredentialBinding
	err := DB.Where("namespace = ? AND account_binding_id = ? AND credential_ref = ?", VantageIntegrationNamespace, account.Id, credentialRef).Take(&credential).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && credential.TokenId == nil {
		return 0, ErrVantageCredentialNotFound
	}
	if err != nil {
		return 0, err
	}
	var token Token
	err = DB.Unscoped().Select("id").Where("id = ? AND user_id = ?", *credential.TokenId, account.UserId).Take(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrVantageCredentialNotFound
	}
	return token.Id, err
}

// A committed outcome can be delivered only after its cache is invalidated.
// Retrying the persisted operation repairs delivery without changing balances.
func invalidateVantageQuotaOperation(operation *VantageTokenQuotaOperation) error {
	if operation.Status != "applied" {
		return nil
	}
	var token Token
	err := DB.Unscoped().Select("key").Where("id = ?", operation.TokenId).Take(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return invalidateTokenCacheForMutation(token.Key)
}

func GetVantageTokenQuotaOperation(tenant, operationID string) (*VantageTokenQuotaOperation, error) {
	account, err := GetVantageAccountBinding(VantageAccountTypeEnterprise, tenant)
	if err != nil {
		return nil, err
	}
	var operation VantageTokenQuotaOperation
	err = DB.Where("namespace = ? AND account_binding_id = ? AND operation_id = ?", VantageIntegrationNamespace, account.Id, operationID).Take(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVantageQuotaOperationNotFound
	}
	if err == nil {
		err = invalidateVantageQuotaOperation(&operation)
	}
	return &operation, err
}

// Native consumption preserves used+remain, so it does not change the policy
// revision. Out-of-band native quota edits are detected by their policy digest.
func SetVantageTokenQuota(tenant, credentialRef string, limit int64, request VantageTokenQuotaOperation) (*VantageTokenQuotaOperation, error) {
	var outcome *VantageTokenQuotaOperation
	var err error
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			account, user, err := vantageQuotaAccount(lockForUpdate(tx), tenant)
			if err != nil {
				return err
			}
			var stored VantageTokenQuotaOperation
			lookup := lockForUpdate(tx).Where("namespace = ? AND operation_id = ?", VantageIntegrationNamespace, request.OperationId).Take(&stored).Error
			if lookup == nil {
				if stored.AccountBindingId != account.Id || stored.RequestDigest != request.RequestDigest {
					return ErrVantageQuotaOperationConflict
				}
				outcome = &stored
				return nil
			}
			if !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return lookup
			}
			if account.Status != VantageAccountStatusActive || user.Status != common.UserStatusEnabled {
				return ErrVantageAccountNotActive
			}
			_, token, err := vantageQuotaToken(tx, account, credentialRef)
			if err != nil {
				return err
			}
			revisions, err := vantageTokenQuotaPolicies(tx, []Token{*token})
			if err != nil {
				return err
			}
			revision := revisions[token.Id]
			request.Namespace, request.AccountBindingId, request.TokenId = VantageIntegrationNamespace, account.Id, token.Id
			request.TenantRef, request.CredentialRef = tenant, credentialRef
			request.Status, request.AppliedRevision, request.ReasonCode = "rejected", nil, nil
			reason := "revision_conflict"
			switch {
			case revision != request.ExpectedRevision:
				request.ReasonCode = &reason
			case token.DeletedAt.Valid || (token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp():
				reason = "credential_not_active"
				request.ReasonCode = &reason
			case limit < token.UsedQuota:
				reason = "limit_below_accounted_usage"
				request.ReasonCode = &reason
			default:
				if limit < 0 || limit > common.MaxTokenQuota || revision >= common.MaxWalletQuota {
					return ErrVantageQuotaInvalidState
				}
				if err := invalidateTokenCacheForMutation(token.Key); err != nil {
					return err
				}
				remaining := limit - token.UsedQuota
				updates := map[string]any{"remain_quota": remaining, "unlimited_quota": false}
				if token.Status == common.TokenStatusExhausted && remaining > 0 {
					updates["status"] = common.TokenStatusEnabled
				}
				if err := tx.Model(&Token{}).Where("id = ? AND user_id = ?", token.Id, account.UserId).Updates(updates).Error; err != nil {
					return err
				}
				token.RemainQuota, token.UnlimitedQuota = remaining, false
				// Explicitly increment even when an administrator re-applies the same limit.
				digest := sha256.Sum256([]byte(fmt.Sprintf("limited:%d", limit)))
				next := revision + 1
				if err := tx.Model(&VantageTokenQuotaConfig{}).Where("token_id = ?", token.Id).Updates(map[string]any{"revision": next, "policy_digest": hex.EncodeToString(digest[:])}).Error; err != nil {
					return err
				}
				request.Status, request.AppliedRevision = "applied", &next
			}
			if err := tx.Create(&request).Error; err != nil {
				return err
			}
			outcome = &request
			return nil
		})
		if err == nil || attempt > 0 || !isVantageRetryableWriteError(err) {
			break
		}
		request.Id = 0
	}
	if err == nil {
		err = invalidateVantageQuotaOperation(outcome)
	}
	return outcome, err
}

// Only bounded native projections are loaded. No complete employee directory.
func ListVantageTokenQuotas(tenant string, after, high int, size int) ([]VantageTokenQuotaSnapshot, int, error) {
	snapshots := []VantageTokenQuotaSnapshot{}
	var maximum int
	err := DB.Transaction(func(tx *gorm.DB) error {
		account, user, err := vantageQuotaAccount(tx, tenant)
		if err != nil {
			return err
		}
		query := tx.Model(&VantageCredentialBinding{}).Where("namespace = ? AND account_binding_id = ? AND token_id IS NOT NULL", VantageIntegrationNamespace, account.Id)
		if err := query.Session(&gorm.Session{}).Select("COALESCE(MAX(id), 0)").Scan(&maximum).Error; err != nil {
			return err
		}
		if high > 0 {
			maximum = min(maximum, high)
		}
		var credentials []VantageCredentialBinding
		if err := query.Where("id > ? AND id <= ?", after, maximum).Order("id ASC").Limit(size).Find(&credentials).Error; err != nil {
			return err
		}
		ids := make([]int, 0, len(credentials))
		for _, credential := range credentials {
			ids = append(ids, *credential.TokenId)
		}
		var tokens []Token
		if len(ids) > 0 {
			if err := lockForUpdate(tx.Unscoped()).Where("id IN ? AND user_id = ?", ids, account.UserId).Order("id ASC").Find(&tokens).Error; err != nil {
				return err
			}
		}
		revisions, err := vantageTokenQuotaPolicies(tx, tokens)
		if err != nil {
			return err
		}
		byID := make(map[int]*Token, len(tokens))
		for i := range tokens {
			byID[tokens[i].Id] = &tokens[i]
		}
		for _, credential := range credentials {
			token := byID[*credential.TokenId]
			if token == nil {
				return ErrVantageCredentialNotFound
			}
			snapshots = append(snapshots, VantageTokenQuotaSnapshot{Account: account, User: user, Credential: &credential, Token: token, Revision: revisions[token.Id]})
		}
		return nil
	})
	return snapshots, maximum, err
}
