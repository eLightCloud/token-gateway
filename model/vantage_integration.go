package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Vantage 集成的持久事实模型：账户绑定、凭据绑定与凭据轮换操作。
// 设计约束见 docs/80-dev/2026-10-14-睿观系统模型接入方案.md 第 5 节：
// 只使用字符串有界引用、整数金额和普通 TEXT，避免方言专属 JSON、
// 部分索引和无界状态 payload，保证 SQLite / MySQL / PostgreSQL 一致。
//
// 并发裁决方式：同一账户的操作在事务内用 lockForUpdate 锁定账户绑定行
// 串行化；跨进程首建冲突由唯一约束兜底，调用方读回已提交事实完成幂等恢复。
// 员工关系的开通与撤销共享同一把账户行锁，因此撤销先到则拒绝开户、
// 开户先到则撤销已创建 Token，两者不可能交错丢失。

const (
	// VantageIntegrationNamespace 应用命名空间由服务端接入口令决定，
	// 不接受客户端指定，避免跨应用认领账户或凭据。
	VantageIntegrationNamespace = "vantage"

	VantageAccountTypePersonal   = "personal"
	VantageAccountTypeEnterprise = "enterprise"

	VantageAccountStatusActive   = "active"
	VantageAccountStatusDisabled = "disabled"

	VantageCredentialStatusActive  = "active"
	VantageCredentialStatusRotated = "rotated"
	VantageCredentialStatusRevoked = "revoked"
)

// 集成内部语义错误：service 层负责映射为稳定错误码与 HTTP 状态。
var (
	ErrVantageAccountNotFound        = errors.New("vantage account binding not found")
	ErrVantageCredentialNotFound     = errors.New("vantage credential binding not found")
	ErrVantageRotationNotFound       = errors.New("vantage rotation operation not found")
	ErrVantageAccountNotActive       = errors.New("vantage managed account is not active")
	ErrVantageCredentialNotActive    = errors.New("vantage credential is not active")
	ErrVantageRelationshipRevoked    = errors.New("vantage membership relationship was revoked")
	ErrVantageGenerationConflict     = errors.New("vantage credential generation conflict")
	ErrVantageRotationIntentConflict = errors.New("vantage rotation idempotency conflict")
)

// VantageGrantFact 个人一次性赠送事实。提交后不再随配置变化重算，
// 企业绑定的赠送事实保持零值。
type VantageGrantFact struct {
	GrantCNY          string // 赠送人民币金额快照
	GrantExchangeRate string // 换算汇率快照
	Quota             int64  // 实际入账 quota
	OpId              string // 赠送操作标识
	Time              int64
}

// VantageAccountBinding 把一个外部主体或租户绑定到一个内部受管计费账户。
// 命名空间＋账户类型＋外部引用唯一；内部受管账户绑定唯一。
type VantageAccountBinding struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	Namespace   string `json:"namespace" gorm:"type:varchar(32);uniqueIndex:uk_vantage_account_ref,priority:1"`
	AccountType string `json:"account_type" gorm:"type:varchar(16);uniqueIndex:uk_vantage_account_ref,priority:2"`
	ExternalRef string `json:"external_ref" gorm:"type:varchar(128);uniqueIndex:uk_vantage_account_ref,priority:3"`
	UserId      int    `json:"user_id" gorm:"uniqueIndex:uk_vantage_account_user"`
	Status      string `json:"status" gorm:"type:varchar(16)"`

	GrantCNY          string `json:"grant_cny" gorm:"type:varchar(32)"`
	GrantExchangeRate string `json:"grant_exchange_rate" gorm:"type:varchar(32)"`
	GrantQuota        int64  `json:"grant_quota" gorm:"type:bigint;default:0"`
	GrantTime         int64  `json:"grant_time" gorm:"bigint;default:0"`
	GrantOpId         string `json:"grant_op_id" gorm:"type:varchar(64)"`

	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt int64 `json:"updated_at" gorm:"autoUpdateTime"`
}

func (VantageAccountBinding) TableName() string { return "vantage_account_bindings" }

// VantageCredentialBinding 保存一段主体/成员关系每个代次的凭据事实。
// 可空 TokenId：撤销先于开通到达时，以 revoked 占位行持久保存撤销事实。
// 同一关系每个代次唯一；每个 Token 只对应一个历史绑定。
type VantageCredentialBinding struct {
	Id               int    `json:"id" gorm:"primaryKey"`
	Namespace        string `json:"namespace" gorm:"type:varchar(32);uniqueIndex:uk_vantage_cred_rel,priority:1"`
	AccountBindingId int    `json:"account_binding_id" gorm:"uniqueIndex:uk_vantage_cred_rel,priority:2;index"`
	Subject          string `json:"subject" gorm:"type:varchar(128);uniqueIndex:uk_vantage_cred_rel,priority:3"`
	MembershipRef    string `json:"membership_ref" gorm:"type:varchar(128);uniqueIndex:uk_vantage_cred_rel,priority:4"`
	Generation       int    `json:"generation" gorm:"uniqueIndex:uk_vantage_cred_rel,priority:5"`
	CredentialRef    string `json:"credential_ref" gorm:"type:varchar(64);uniqueIndex"`
	TokenId          *int   `json:"token_id" gorm:"uniqueIndex:uk_vantage_cred_token"`
	Status           string `json:"status" gorm:"type:varchar(16)"`

	CreatedAt int64 `json:"created_at" gorm:"autoCreateTime"`
	EndedAt   int64 `json:"ended_at" gorm:"bigint;default:0"` // 轮换或撤销时间，0 表示仍有效
}

func (VantageCredentialBinding) TableName() string { return "vantage_credential_bindings" }

// VantageCredentialRotation 保存一次轮换操作的幂等事实，作为响应丢失后的
// 恢复依据。同一操作标识唯一；同一标识配不同业务对象或意图必须拒绝。
type VantageCredentialRotation struct {
	Id                  int    `json:"id" gorm:"primaryKey"`
	Namespace           string `json:"namespace" gorm:"type:varchar(32);uniqueIndex:uk_vantage_rotate_op,priority:1"`
	OpId                string `json:"op_id" gorm:"type:varchar(128);uniqueIndex:uk_vantage_rotate_op,priority:2"`
	CredentialRef       string `json:"credential_ref" gorm:"type:varchar(64);index"`
	ExpectedGeneration  int    `json:"expected_generation"` // 0 表示未指定预期代次
	IntentDigest        string `json:"intent_digest" gorm:"type:varchar(64)"`
	ResultCredentialRef string `json:"result_credential_ref" gorm:"type:varchar(64)"`
	ResultGeneration    int    `json:"result_generation"`
	CreatedAt           int64  `json:"created_at" gorm:"autoCreateTime"`
}

func (VantageCredentialRotation) TableName() string { return "vantage_credential_rotations" }

// isVantageRetryableWriteError 识别并发首建时的可重试写冲突：三种数据库的
// 唯一约束冲突，以及 MySQL 空隙锁死锁（1213）与 SQLite BUSY（锁竞争）。
// 命中后由调用方的单次重试读取其他事务已提交的事实，完成幂等恢复。
func isVantageRetryableWriteError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") ||
		strings.Contains(message, "unique") ||
		strings.Contains(message, "1062") ||
		strings.Contains(message, "deadlock") ||
		strings.Contains(message, "1213") ||
		strings.Contains(message, "locked")
}

// vantageManagedUsername 生成确定性的内部用户名（12 字符，满足
// UserNameMaxLength 与唯一性约束），便于运营把受管账户与外部引用对应。
func vantageManagedUsername(accountType, externalRef string) string {
	digest := sha256.Sum256([]byte(accountType + "|" + externalRef))
	return "vg" + hex.EncodeToString(digest[:])[:10]
}

// newVantageManagedUser 创建受管计费账户：固定普通角色，不设本地密码
// （ValidateAndFill 对空密码直接失败，接入口令无法用于登录这些账户）。
// 初始额度完全由调用方决定，不经过通用 QuotaForNewUser 赠送路径。
func newVantageManagedUser(accountType, externalRef, displayName string, quota int64) *User {
	return &User{
		Username:    vantageManagedUsername(accountType, externalRef),
		Password:    "",
		DisplayName: displayName,
		Role:        common.RoleCommonUser,
		Status:      common.UserStatusEnabled,
		Quota:       quota,
		AffCode:     common.GetRandomString(8),
	}
}

// newVantageManagedToken 创建受管凭据对应的默认 Token：不限额度、永不过期，
// 余额由所属账户钱包约束，不以 Token 无限额度代表账户余额无限。
func newVantageManagedToken(userId int) (*Token, error) {
	key, err := common.GenerateKey()
	if err != nil {
		return nil, err
	}
	now := common.GetTimestamp()
	return &Token{
		UserId:         userId,
		Name:           "vantage",
		Key:            key,
		CreatedTime:    now,
		AccessedTime:   now,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
		Status:         common.TokenStatusEnabled,
	}, nil
}

func GetVantageAccountBinding(accountType, externalRef string) (*VantageAccountBinding, error) {
	var binding VantageAccountBinding
	err := DB.Where("namespace = ? AND account_type = ? AND external_ref = ?",
		VantageIntegrationNamespace, accountType, externalRef).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVantageAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// VantageCredentialAuthenticationActive 查询原生 Token 与计费账户的当前状态。
// 绑定 active 不能代替实际认证资格：后台停卡、到期和账户停用必须立即生效。
// 仅读取非秘密列，不改变撤销事实，不因恢复启用而生成第二个 Key。
func VantageCredentialAuthenticationActive(binding *VantageCredentialBinding, userId int) (bool, error) {
	if binding.Status != VantageCredentialStatusActive || binding.TokenId == nil {
		return false, nil
	}
	var token struct {
		Status      int
		ExpiredTime int64
	}
	err := DB.Model(&Token{}).Select("status", "expired_time").Where("id = ? AND user_id = ?", *binding.TokenId, userId).Take(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if token.Status != common.TokenStatusEnabled || (token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp()) {
		return false, nil
	}
	var user struct{ Status int }
	err = DB.Model(&User{}).Select("status").Where("id = ?", userId).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return user.Status == common.UserStatusEnabled, nil
}

func GetVantageAccountBindingById(id int) (*VantageAccountBinding, error) {
	return vantageAccountByIdRead(DB, id)
}

// vantageAccountByIdRead 支持传入 tx，保证事务内的读取不落到全局连接。
func vantageAccountByIdRead(reader *gorm.DB, id int) (*VantageAccountBinding, error) {
	var binding VantageAccountBinding
	err := reader.First(&binding, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVantageAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func GetVantageCredentialBinding(credentialRef string) (*VantageCredentialBinding, error) {
	return vantageCredentialRefRead(DB, credentialRef)
}

// vantageCredentialRefRead 支持传入 tx，保证事务内的读取不落到全局连接。
func vantageCredentialRefRead(reader *gorm.DB, credentialRef string) (*VantageCredentialBinding, error) {
	var binding VantageCredentialBinding
	err := reader.Where("namespace = ? AND credential_ref = ?",
		VantageIntegrationNamespace, credentialRef).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVantageCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func GetVantageCredentialBindingsByAccount(accountBindingId int) ([]VantageCredentialBinding, error) {
	var bindings []VantageCredentialBinding
	err := DB.Where("namespace = ? AND account_binding_id = ?",
		VantageIntegrationNamespace, accountBindingId).
		Order("subject ASC, membership_ref ASC, generation DESC").Find(&bindings).Error
	return bindings, err
}

func GetVantageRotationOp(opId string) (*VantageCredentialRotation, error) {
	var op VantageCredentialRotation
	err := DB.Where("namespace = ? AND op_id = ?",
		VantageIntegrationNamespace, opId).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVantageRotationNotFound
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func vantageAccountForUpdate(tx *gorm.DB, accountType, externalRef string) (*VantageAccountBinding, bool, error) {
	// 不对不存在的资源获取 MySQL 空隙锁：首建由唯一约束裁决，避免并发
	// 请求各持共享空隙锁后同时 INSERT 而反复死锁。已有资源仍锁账户行。
	var probe VantageAccountBinding
	probeErr := tx.Select("id").Where("namespace = ? AND account_type = ? AND external_ref = ?",
		VantageIntegrationNamespace, accountType, externalRef).First(&probe).Error
	if errors.Is(probeErr, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if probeErr != nil {
		return nil, false, probeErr
	}
	var binding VantageAccountBinding
	err := lockForUpdate(tx).Where("namespace = ? AND account_type = ? AND external_ref = ?",
		VantageIntegrationNamespace, accountType, externalRef).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &binding, true, nil
}

func vantageCredentialForUpdate(tx *gorm.DB, credentialRef string) (*VantageCredentialBinding, bool, error) {
	// 先锁不可变归属所指向的账户，再锁凭据；与成员确保/撤销保持同一锁序。
	accountID := tx.Model(&VantageCredentialBinding{}).Select("account_binding_id").
		Where("namespace = ? AND credential_ref = ?", VantageIntegrationNamespace, credentialRef)
	var account VantageAccountBinding
	if err := lockForUpdate(tx).Where("id IN (?)", accountID).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var binding VantageCredentialBinding
	err := lockForUpdate(tx).Where("namespace = ? AND credential_ref = ?",
		VantageIntegrationNamespace, credentialRef).First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &binding, true, nil
}

// vantageRelationshipCredential 读取一段关系的当前（最大代次）凭据事实。
func vantageRelationshipCredential(tx *gorm.DB, accountBindingId int, subject, membershipRef string) (*VantageCredentialBinding, bool, error) {
	var binding VantageCredentialBinding
	err := lockForUpdate(tx).Where("namespace = ? AND account_binding_id = ? AND subject = ? AND membership_ref = ?",
		VantageIntegrationNamespace, accountBindingId, subject, membershipRef).
		Order("generation DESC").First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &binding, true, nil
}

func vantageCredentialToken(reader *gorm.DB, binding *VantageCredentialBinding) (*Token, error) {
	if binding.TokenId == nil {
		return nil, nil
	}
	var token Token
	if err := reader.First(&token, *binding.TokenId).Error; err != nil {
		return nil, err
	}
	return &token, nil
}

func vantageInsertCredentialBinding(tx *gorm.DB, binding *VantageCredentialBinding) error {
	key, err := common.GenerateRandomCharsKey(32)
	if err != nil {
		return err
	}
	binding.CredentialRef = "vc-" + key
	return tx.Create(binding).Error
}

// vantageDisableCredential 在事务内停用凭据对应的 Token 并落撤销事实。
// 写入前建立既有缓存 fence，提交后及幂等恢复时再次失效。
func vantageDisableCredential(tx *gorm.DB, binding *VantageCredentialBinding, status string) error {
	if binding.TokenId != nil {
		token, err := vantageCredentialToken(lockForUpdate(tx), binding)
		if err != nil {
			return err
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return err
		}
		if err := tx.Model(&Token{}).Where("id = ?", *binding.TokenId).
			Update("status", common.TokenStatusDisabled).Error; err != nil {
			return err
		}
	}
	return tx.Model(&VantageCredentialBinding{}).Where("id = ?", binding.Id).
		Updates(map[string]any{"status": status, "ended_at": common.GetTimestamp()}).Error
}

// VantageEnsurePersonalAccount 以稳定主体引用幂等确保个人受管账户：
// 同一主库事务写入 User、个人账户绑定、一次性赠送事实和默认凭据绑定，
// 不经过通用全局赠送后处理。重复请求读取已提交事实并返回相同结果。
func VantageEnsurePersonalAccount(subject, displayName string, grant VantageGrantFact) (acct *VantageAccountBinding, cred *VantageCredentialBinding, token *Token, created bool, err error) {
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			existing, found, terr := vantageAccountForUpdate(tx, VantageAccountTypePersonal, subject)
			if terr != nil {
				return terr
			}
			if found {
				acct = existing
				var foundCred bool
				cred, foundCred, terr = vantageRelationshipCredential(tx, existing.Id, subject, "")
				if terr != nil {
					return terr
				}
				if !foundCred {
					return ErrVantageCredentialNotFound
				}
				token, terr = vantageCredentialToken(lockForUpdate(tx), cred)
				return terr
			}
			user := newVantageManagedUser(VantageAccountTypePersonal, subject, displayName, grant.Quota)
			if err := tx.Create(user).Error; err != nil {
				return err
			}
			acct = &VantageAccountBinding{
				Namespace:         VantageIntegrationNamespace,
				AccountType:       VantageAccountTypePersonal,
				ExternalRef:       subject,
				UserId:            user.Id,
				Status:            VantageAccountStatusActive,
				GrantCNY:          grant.GrantCNY,
				GrantExchangeRate: grant.GrantExchangeRate,
				GrantQuota:        grant.Quota,
				GrantTime:         grant.Time,
				GrantOpId:         grant.OpId,
			}
			if err := tx.Create(acct).Error; err != nil {
				return err
			}
			token, terr = newVantageManagedToken(user.Id)
			if terr != nil {
				return terr
			}
			if err := tx.Create(token).Error; err != nil {
				return err
			}
			cred = &VantageCredentialBinding{
				Namespace:        VantageIntegrationNamespace,
				AccountBindingId: acct.Id,
				Subject:          subject,
				Generation:       1,
				TokenId:          &token.Id,
				Status:           VantageCredentialStatusActive,
			}
			if err := vantageInsertCredentialBinding(tx, cred); err != nil {
				return err
			}
			created = true
			return nil
		})
		if err == nil {
			return acct, cred, token, created, nil
		}
		// 并发首建撞唯一约束时重试一次：第二轮读取其他事务已提交的事实，
		// 走恢复分支返回相同账户与凭据，绝不重复创建或重复赠送。
		if attempt == 0 && isVantageRetryableWriteError(err) {
			acct, cred, token, created = nil, nil, nil, false
			continue
		}
		break
	}
	return nil, nil, nil, false, err
}

// VantageEnsureEnterpriseAccount 以稳定租户引用幂等确保企业受管账户：
// 只创建零初始额度的 User 和企业绑定，不赠送、不创建凭据。
// 重试不覆盖已有账户、运营充值或状态。
func VantageEnsureEnterpriseAccount(tenant, displayName string) (acct *VantageAccountBinding, created bool, err error) {
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			existing, found, terr := vantageAccountForUpdate(tx, VantageAccountTypeEnterprise, tenant)
			if terr != nil {
				return terr
			}
			if found {
				acct = existing
				return nil
			}
			user := newVantageManagedUser(VantageAccountTypeEnterprise, tenant, displayName, 0)
			if err := tx.Create(user).Error; err != nil {
				return err
			}
			acct = &VantageAccountBinding{
				Namespace:   VantageIntegrationNamespace,
				AccountType: VantageAccountTypeEnterprise,
				ExternalRef: tenant,
				UserId:      user.Id,
				Status:      VantageAccountStatusActive,
			}
			if err := tx.Create(acct).Error; err != nil {
				return err
			}
			created = true
			return nil
		})
		if err == nil {
			return acct, created, nil
		}
		if attempt == 0 && isVantageRetryableWriteError(err) {
			acct, created = nil, false
			continue
		}
		break
	}
	return nil, false, err
}

// VantageEnsureMemberCredential 为当前成员关系幂等确保一个员工 Key。
// 关系已撤销（含撤销先于开通到达的占位事实）时拒绝开户；
// 已存在有效凭据时恢复同一凭据，响应丢失的重试不会生成新 Key。
func VantageEnsureMemberCredential(tenant, subject, membershipRef string) (acct *VantageAccountBinding, cred *VantageCredentialBinding, token *Token, created bool, err error) {
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			existing, found, terr := vantageAccountForUpdate(tx, VantageAccountTypeEnterprise, tenant)
			if terr != nil {
				return terr
			}
			if !found {
				return ErrVantageAccountNotFound
			}
			if existing.Status != VantageAccountStatusActive {
				return ErrVantageAccountNotActive
			}
			acct = existing
			current, has, terr := vantageRelationshipCredential(tx, existing.Id, subject, membershipRef)
			if terr != nil {
				return terr
			}
			if has {
				if current.Status == VantageCredentialStatusRevoked {
					return ErrVantageRelationshipRevoked
				}
				cred = current
				token, terr = vantageCredentialToken(lockForUpdate(tx), cred)
				return terr
			}
			token, terr = newVantageManagedToken(existing.UserId)
			if terr != nil {
				return terr
			}
			if err := tx.Create(token).Error; err != nil {
				return err
			}
			cred = &VantageCredentialBinding{
				Namespace:        VantageIntegrationNamespace,
				AccountBindingId: existing.Id,
				Subject:          subject,
				MembershipRef:    membershipRef,
				Generation:       1,
				TokenId:          &token.Id,
				Status:           VantageCredentialStatusActive,
			}
			if err := vantageInsertCredentialBinding(tx, cred); err != nil {
				return err
			}
			created = true
			return nil
		})
		if err == nil {
			return acct, cred, token, created, nil
		}
		if attempt == 0 && isVantageRetryableWriteError(err) {
			acct, cred, token, created = nil, nil, nil, false
			continue
		}
		break
	}
	return nil, nil, nil, false, err
}

// VantageRotationOutcome 描述一次轮换的结果；Created=false 表示按同一
// 操作标识恢复的既有结果。NewToken 只在新代次有效时非空。
type VantageRotationOutcome struct {
	Op            *VantageCredentialRotation
	OldCredential *VantageCredentialBinding
	NewCredential *VantageCredentialBinding
	NewToken      *Token
	OldTokenKey   string
	Created       bool
}

// vantageRotationRecovery 校验已提交轮换操作与本请求意图一致后，
// 返回其持久化结果（相同新凭据与相同 Key）。
func vantageRotationRecovery(reader *gorm.DB, op *VantageCredentialRotation, credentialRef, intentDigest string, expectedGeneration int) (*VantageRotationOutcome, error) {
	if op.CredentialRef != credentialRef || op.IntentDigest != intentDigest || op.ExpectedGeneration != expectedGeneration {
		return nil, ErrVantageRotationIntentConflict
	}
	// 保留事务和当前读锁，但每次查询独立建链，避免 GORM 复用查询条件。
	reader = reader.Session(&gorm.Session{})
	newCred, err := vantageCredentialRefRead(reader, op.ResultCredentialRef)
	if err != nil {
		return nil, err
	}
	oldCred, err := vantageCredentialRefRead(reader, credentialRef)
	if err != nil {
		return nil, err
	}
	token, err := vantageCredentialToken(reader, newCred)
	if err != nil {
		return nil, err
	}
	oldToken, err := vantageCredentialToken(reader, oldCred)
	if err != nil {
		return nil, err
	}
	if oldToken != nil {
		if err := invalidateTokenCacheForMutation(oldToken.Key); err != nil {
			return nil, err
		}
	}
	return &VantageRotationOutcome{
		Op:            op,
		OldCredential: oldCred,
		NewCredential: newCred,
		NewToken:      token,
		Created:       false,
	}, nil
}

// VantageRotateCredential 幂等轮换一个凭据：创建下一代 Token、停用旧
// Token、旧绑定转 rotated，并以操作标识持久保存恢复依据。
// 响应丢失的重试恢复同一结果；旧代次重放不能重新激活或再开一个 Key。
func VantageRotateCredential(credentialRef, opId, intentDigest string, expectedGeneration int) (*VantageRotationOutcome, error) {
	if op, err := GetVantageRotationOp(opId); err == nil {
		return vantageRotationRecovery(DB, op, credentialRef, intentDigest, expectedGeneration)
	} else if !errors.Is(err, ErrVantageRotationNotFound) {
		return nil, err
	}

	var outcome *VantageRotationOutcome
	var err error
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			cred, found, terr := vantageCredentialForUpdate(tx, credentialRef)
			if terr != nil {
				return terr
			}
			if !found {
				return ErrVantageCredentialNotFound
			}
			// 等锁后必须使用当前读：MySQL 子查询可能已建立旧快照。
			// 同操作在等待期间提交后，轮换记录及结果都需读取新提交事实。
			var existingOp VantageCredentialRotation
			opErr := lockForUpdate(tx).Where("namespace = ? AND op_id = ?",
				VantageIntegrationNamespace, opId).First(&existingOp).Error
			if opErr == nil {
				outcome, terr = vantageRotationRecovery(lockForUpdate(tx), &existingOp, credentialRef, intentDigest, expectedGeneration)
				return terr
			}
			if !errors.Is(opErr, gorm.ErrRecordNotFound) {
				return opErr
			}
			if cred.Status != VantageCredentialStatusActive {
				return ErrVantageCredentialNotActive
			}
			if expectedGeneration != 0 && expectedGeneration != cred.Generation {
				return ErrVantageGenerationConflict
			}
			account, terr := vantageAccountByIdRead(lockForUpdate(tx), cred.AccountBindingId)
			if terr != nil {
				return terr
			}
			if account.Status != VantageAccountStatusActive {
				return ErrVantageAccountNotActive
			}
			oldToken, terr := vantageCredentialToken(lockForUpdate(tx), cred)
			if terr != nil {
				return terr
			}
			token, terr := newVantageManagedToken(account.UserId)
			if terr != nil {
				return terr
			}
			if err := tx.Create(token).Error; err != nil {
				return err
			}
			newCred := &VantageCredentialBinding{
				Namespace:        VantageIntegrationNamespace,
				AccountBindingId: cred.AccountBindingId,
				Subject:          cred.Subject,
				MembershipRef:    cred.MembershipRef,
				Generation:       cred.Generation + 1,
				TokenId:          &token.Id,
				Status:           VantageCredentialStatusActive,
			}
			if err := vantageInsertCredentialBinding(tx, newCred); err != nil {
				return err
			}
			if err := vantageDisableCredential(tx, cred, VantageCredentialStatusRotated); err != nil {
				return err
			}
			op := &VantageCredentialRotation{
				Namespace:           VantageIntegrationNamespace,
				OpId:                opId,
				CredentialRef:       credentialRef,
				ExpectedGeneration:  expectedGeneration,
				IntentDigest:        intentDigest,
				ResultCredentialRef: newCred.CredentialRef,
				ResultGeneration:    newCred.Generation,
			}
			if err := tx.Create(op).Error; err != nil {
				return err
			}
			outcome = &VantageRotationOutcome{
				Op:            op,
				OldCredential: cred,
				NewCredential: newCred,
				NewToken:      token,
				Created:       true,
			}
			if oldToken != nil {
				outcome.OldTokenKey = oldToken.Key
			}
			return nil
		})
		if err == nil {
			// 提交后失效旧代次 Token 缓存，旧 Key 的下一个请求立即被拒绝。
			if outcome.OldTokenKey != "" {
				if cacheErr := invalidateTokenCacheForMutation(outcome.OldTokenKey); cacheErr != nil {
					return nil, cacheErr
				}
			}
			return outcome, nil
		}
		if attempt == 0 && isVantageRetryableWriteError(err) {
			continue
		}
		break
	}
	return nil, err
}

// VantageRevokeCredential 幂等撤销一个凭据：停用对应 Token 并落撤销事实。
// 已撤销或已轮换的凭据保持现状，重复请求不生成新凭据、不复活旧 Key。
func VantageRevokeCredential(credentialRef string) (cred *VantageCredentialBinding, token *Token, changed bool, err error) {
	err = DB.Transaction(func(tx *gorm.DB) error {
		existing, found, terr := vantageCredentialForUpdate(tx, credentialRef)
		if terr != nil {
			return terr
		}
		if !found {
			return ErrVantageCredentialNotFound
		}
		cred = existing
		token, terr = vantageCredentialToken(lockForUpdate(tx), cred)
		if terr != nil {
			return terr
		}
		if cred.Status != VantageCredentialStatusActive {
			return nil
		}
		if err := vantageDisableCredential(tx, cred, VantageCredentialStatusRevoked); err != nil {
			return err
		}
		cred.Status = VantageCredentialStatusRevoked
		changed = true
		return nil
	})
	if err == nil && token != nil {
		if cacheErr := invalidateTokenCacheForMutation(token.Key); cacheErr != nil {
			err = cacheErr
		}
	}
	return cred, token, changed, err
}

// VantageRevokeMembership 幂等撤销整段成员关系。即使 Key 尚未创建，
// 也以 revoked 占位绑定持久保存撤销事实；此后同关系开通一律被拒绝。
func VantageRevokeMembership(tenant, subject, membershipRef string) (cred *VantageCredentialBinding, token *Token, changed bool, err error) {
	err = DB.Transaction(func(tx *gorm.DB) error {
		acct, found, terr := vantageAccountForUpdate(tx, VantageAccountTypeEnterprise, tenant)
		if terr != nil {
			return terr
		}
		if !found {
			return ErrVantageAccountNotFound
		}
		current, has, terr := vantageRelationshipCredential(tx, acct.Id, subject, membershipRef)
		if terr != nil {
			return terr
		}
		if has {
			cred = current
			token, terr = vantageCredentialToken(lockForUpdate(tx), cred)
			if terr != nil {
				return terr
			}
			if cred.Status != VantageCredentialStatusActive {
				return nil
			}
			if err := vantageDisableCredential(tx, cred, VantageCredentialStatusRevoked); err != nil {
				return err
			}
			cred.Status = VantageCredentialStatusRevoked
			changed = true
			return nil
		}
		// 撤销先到：Key 尚不存在，也必须先持久保存关系撤销事实。
		cred = &VantageCredentialBinding{
			Namespace:        VantageIntegrationNamespace,
			AccountBindingId: acct.Id,
			Subject:          subject,
			MembershipRef:    membershipRef,
			Generation:       0,
			Status:           VantageCredentialStatusRevoked,
		}
		if err := vantageInsertCredentialBinding(tx, cred); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && token != nil {
		if cacheErr := invalidateTokenCacheForMutation(token.Key); cacheErr != nil {
			err = cacheErr
		}
	}
	return cred, token, changed, err
}

// VantageModelCatalogItem 是接入目录的安全投影，不含渠道地址、Key 或价格。
// Status 是元数据可见性；RouteAvailable 独立表示至少存在一个启用的渠道能力。
type VantageModelCatalogItem struct {
	ModelName      string   `json:"model_name"`
	Description    string   `json:"description,omitempty"`
	VendorID       int      `json:"vendor_id,omitempty"`
	Status         int      `json:"status"`
	RouteAvailable bool     `json:"route_available"`
	Endpoints      []string `json:"endpoints,omitempty"`
	SyncOfficial   int      `json:"sync_official"`
	UpdatedTime    int64    `json:"updated_time"`
}

// GetVantageModelCatalog 复用现有规则优先级和渠道端点解析，输出具体模型名。
// 内容摘要同时覆盖删除、同秒修改与供应状态变化，分页客户端据此检查版本一致性。
func GetVantageModelCatalog() ([]VantageModelCatalogItem, string, error) {
	var metadata []Model
	if err := DB.Order("id").Find(&metadata).Error; err != nil {
		return nil, "", err
	}
	configured, err := GetConfiguredModelChannels()
	if err != nil {
		return nil, "", err
	}
	for _, row := range metadata {
		if row.NameRule == NameRuleExact {
			configured[row.ModelName] = nil
		}
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	slices.Sort(names)
	resolved := resolveModelMetadata(metadata, names)
	connections, err := GetModelConnections()
	if err != nil {
		return nil, "", err
	}
	abilities := make([]AbilityWithChannel, 0, len(connections))
	for _, connection := range connections {
		abilities = append(abilities, connection.AbilityWithChannel)
	}
	configs := loadPricingAdvancedCustomConfigs(abilities)
	endpoints := make(map[string][]string)
	available := make(map[string]bool)
	for _, ability := range abilities {
		available[ability.Model] = true
		for _, endpoint := range getPricingEndpointTypesForAbility(ability, configs) {
			endpoints[ability.Model] = appendPricingEndpoint(endpoints[ability.Model], string(endpoint))
		}
	}
	items := make([]VantageModelCatalogItem, 0, len(names))
	for _, name := range names {
		item := VantageModelCatalogItem{ModelName: name, Status: 1, RouteAvailable: available[name], Endpoints: endpoints[name]}
		if row := resolved[name]; row != nil {
			item.Description, item.VendorID, item.Status = row.Description, row.VendorID, row.Status
			item.SyncOfficial, item.UpdatedTime = row.SyncOfficial, row.UpdatedTime
			if strings.TrimSpace(row.Endpoints) != "" && ValidateModelEndpoints(row.Endpoints) == nil {
				var value any
				if err := common.UnmarshalJsonStr(row.Endpoints, &value); err != nil {
					return nil, "", err
				}
				switch declared := value.(type) {
				case []any:
					for _, endpoint := range declared {
						item.Endpoints = appendPricingEndpoint(item.Endpoints, endpoint.(string))
					}
				case map[string]any:
					for endpoint := range declared {
						item.Endpoints = appendPricingEndpoint(item.Endpoints, endpoint)
					}
				}
			}
		}
		slices.Sort(item.Endpoints)
		items = append(items, item)
	}
	content, err := common.Marshal(items)
	if err != nil {
		return nil, "", err
	}
	revision := sha256.Sum256(content)
	return items, hex.EncodeToString(revision[:]), nil
}
