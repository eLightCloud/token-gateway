package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/shopspring/decimal"
)

// Vantage 专用接入服务：只接受服务端接入口令，服务端解析内部账户。
// 业务与安全语义见 docs/80-dev/2026-10-14-睿观系统模型接入方案.md。

const (
	// vantagePersonalGrantCNY 个人一次性赠送的业务金额，固定值，
	// 不随用户请求、支付折扣或购买倍率变化。
	vantagePersonalGrantCNY = "100"

	VantageDisplayNameMaxRunes = 20
)

var vantageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// VantageError 携带稳定错误码与 HTTP 状态。Message 必须低敏，
// 不含应用口令、Token 明文或内部细节；内部原因只进服务端日志。
type VantageError struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *VantageError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

const (
	VantageCodeInvalidRequest            = "invalid_request"
	VantageCodeNotFound                  = "not_found"
	VantageCodeInternal                  = "internal_error"
	VantageCodeMembershipRevoked         = "membership_revoked"
	VantageCodeCredentialNotActive       = "credential_not_active"
	VantageCodeGenerationConflict        = "generation_conflict"
	VantageCodeIdempotencyConflict       = "idempotency_conflict"
	VantageCodeAccountNotActive          = "account_not_active"
	VantageCodeGrantConfigurationInvalid = "grant_configuration_invalid"
)

func vantageErrorf(status int, code, message string) *VantageError {
	return &VantageError{Status: status, Code: code, Message: message}
}

func vantageInvalidRequest(message string) *VantageError {
	return vantageErrorf(400, VantageCodeInvalidRequest, message)
}

// VantageInvalidRequest 供 HTTP 层复用同一稳定错误码做请求体与参数校验。
func VantageInvalidRequest(message string) error {
	return vantageInvalidRequest(message)
}

// vantageErrorFromModel 把集成内部语义错误映射为稳定错误码。
func vantageErrorFromModel(err error) error {
	switch {
	case errors.Is(err, model.ErrVantageAccountNotFound),
		errors.Is(err, model.ErrVantageCredentialNotFound),
		errors.Is(err, model.ErrVantageRotationNotFound):
		return vantageErrorf(404, VantageCodeNotFound, "unknown managed object")
	case errors.Is(err, model.ErrVantageRelationshipRevoked):
		return vantageErrorf(409, VantageCodeMembershipRevoked, "membership relationship was revoked")
	case errors.Is(err, model.ErrVantageCredentialNotActive):
		return vantageErrorf(409, VantageCodeCredentialNotActive, "credential is not active")
	case errors.Is(err, model.ErrVantageGenerationConflict):
		return vantageErrorf(409, VantageCodeGenerationConflict, "credential generation conflict")
	case errors.Is(err, model.ErrVantageRotationIntentConflict):
		return vantageErrorf(409, VantageCodeIdempotencyConflict, "idempotency key conflicts with a different operation")
	case errors.Is(err, model.ErrVantageAccountNotActive):
		return vantageErrorf(409, VantageCodeAccountNotActive, "managed account is not active")
	default:
		return &VantageError{Status: 500, Code: VantageCodeInternal, Message: "internal error", Err: err}
	}
}

func validateVantageRef(field, value string) error {
	if !vantageRefPattern.MatchString(value) {
		return vantageInvalidRequest(field + " must match [A-Za-z0-9][A-Za-z0-9._:-]{0,127}")
	}
	return nil
}

func validateVantageDisplayName(displayName string) error {
	if utf8.RuneCountInString(displayName) > VantageDisplayNameMaxRunes {
		return vantageInvalidRequest(fmt.Sprintf("display_name must not exceed %d characters", VantageDisplayNameMaxRunes))
	}
	return nil
}

// vantagePersonalGrant 按现有人民币余额展示口径换算一次性赠送：
// 100 CNY / USDExchangeRate * QuotaPerUnit，decimal 运算并用
// common.WalletQuotaFromDecimalStrict 严格校验范围。配置非法、结果
// 越界或非正时拒绝开户，不能悄悄少送或送零。换算快照随赠送事实持久化，
// 事实提交后不随配置变化重算。
func vantagePersonalGrant() (model.VantageGrantFact, error) {
	if operation_setting.USDExchangeRate <= 0 || math.IsNaN(operation_setting.USDExchangeRate) || math.IsInf(operation_setting.USDExchangeRate, 0) {
		return model.VantageGrantFact{}, vantageErrorf(500, VantageCodeGrantConfigurationInvalid, "USD exchange rate must be finite and positive")
	}
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return model.VantageGrantFact{}, vantageErrorf(500, VantageCodeGrantConfigurationInvalid, "quota per unit must be finite and positive")
	}
	amountCNY, err := decimal.NewFromString(vantagePersonalGrantCNY)
	if err != nil {
		return model.VantageGrantFact{}, vantageErrorf(500, VantageCodeGrantConfigurationInvalid, "invalid grant amount")
	}
	quotaDecimal := amountCNY.
		Div(decimal.NewFromFloat(operation_setting.USDExchangeRate)).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	quota, err := common.WalletQuotaFromDecimalStrict(quotaDecimal)
	if err != nil {
		return model.VantageGrantFact{}, vantageErrorf(500, VantageCodeGrantConfigurationInvalid, "grant quota out of representable range")
	}
	if quota <= 0 {
		return model.VantageGrantFact{}, vantageErrorf(500, VantageCodeGrantConfigurationInvalid, "grant quota must be positive")
	}
	return model.VantageGrantFact{
		GrantCNY:          vantagePersonalGrantCNY,
		GrantExchangeRate: strconv.FormatFloat(operation_setting.USDExchangeRate, 'f', -1, 64),
		Quota:             quota,
		OpId:              "vg-" + common.GetTimeString(),
		Time:              common.GetTimestamp(),
	}, nil
}

type VantageAccountView struct {
	Ref            string `json:"ref"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	InternalUserId int    `json:"internal_user_id"`
	GrantQuota     int64  `json:"grant_quota"`
	GrantCNY       string `json:"grant_cny,omitempty"`
	CreatedAt      int64  `json:"created_at"`
}

type VantageCredentialView struct {
	Ref           string `json:"ref"`
	Status        string `json:"status"`
	Generation    int    `json:"generation"`
	Subject       string `json:"subject"`
	MembershipRef string `json:"membership_ref,omitempty"`
	AccountRef    string `json:"account_ref"`
	HasToken      bool   `json:"has_token"`
	CreatedAt     int64  `json:"created_at"`
	EndedAt       int64  `json:"ended_at,omitempty"`
}

// VantageProvisionResult 汇总开户/确保凭据/轮换的对外结果。
// TokenKey 仅在受控的确保与轮换响应中交付，且只在凭据有效时返回；
// 恢复请求返回同一 Key，撤销或轮换后的重放不再回显。
type VantageProvisionResult struct {
	Created    bool                   `json:"created"`
	Account    *VantageAccountView    `json:"account"`
	Credential *VantageCredentialView `json:"credential,omitempty"`
	TokenKey   string                 `json:"-"`
}

func vantageAccountRefOf(binding *model.VantageAccountBinding) string {
	return binding.AccountType + "/" + binding.ExternalRef
}

func newVantageAccountView(binding *model.VantageAccountBinding) *VantageAccountView {
	return &VantageAccountView{
		Ref:            vantageAccountRefOf(binding),
		Type:           binding.AccountType,
		Status:         binding.Status,
		InternalUserId: binding.UserId,
		GrantQuota:     binding.GrantQuota,
		GrantCNY:       binding.GrantCNY,
		CreatedAt:      binding.CreatedAt,
	}
}

func newVantageCredentialView(binding *model.VantageCredentialBinding, account *model.VantageAccountBinding) *VantageCredentialView {
	view := &VantageCredentialView{
		Ref:           binding.CredentialRef,
		Status:        binding.Status,
		Generation:    binding.Generation,
		Subject:       binding.Subject,
		MembershipRef: binding.MembershipRef,
		AccountRef:    vantageAccountRefOf(account),
		HasToken:      binding.TokenId != nil,
		CreatedAt:     binding.CreatedAt,
		EndedAt:       binding.EndedAt,
	}
	return view
}

// vantageTokenKeyForDelivery 仅在凭据有效且存在 Token 时交付 Key；
// 已撤销或已被轮换的凭据不因旧请求重放泄露 Key。
func vantageTokenKeyForDelivery(credential *model.VantageCredentialBinding, token *model.Token) string {
	if credential == nil || token == nil || credential.Status != model.VantageCredentialStatusActive {
		return ""
	}
	return "sk-" + token.Key
}

// VantageProvisionPersonalAccount 确保个人账户及默认 Key。
// 首次创建并一次性赠送；重试、超时与响应丢失恢复同一结果。
func VantageProvisionPersonalAccount(subject, displayName string) (*VantageProvisionResult, error) {
	if err := validateVantageRef("subject", subject); err != nil {
		return nil, err
	}
	if err := validateVantageDisplayName(displayName); err != nil {
		return nil, err
	}
	// 已存在账户时走恢复路径：赠送换算配置不阻塞恢复（A05 响应丢失可恢复），
	// 恢复分支不使用 grant；只有真正首建时才要求换算配置合法。
	grant := model.VantageGrantFact{}
	_, lookupErr := model.GetVantageAccountBinding(model.VantageAccountTypePersonal, subject)
	switch {
	case lookupErr == nil:
		// 账户已存在，ensure 将走恢复分支。
	case errors.Is(lookupErr, model.ErrVantageAccountNotFound):
		grantErr := error(nil)
		grant, grantErr = vantagePersonalGrant()
		if grantErr != nil {
			return nil, grantErr
		}
	default:
		return nil, vantageErrorFromModel(lookupErr)
	}
	acct, cred, token, created, err := model.VantageEnsurePersonalAccount(subject, displayName, grant)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if created {
		// 低敏关联事件：只记录主体引用与赠送额度，不含口令或 Token 明文。
		// 日志库写入失败不影响已提交的赠送事实，也不会导致重新赠送。
		model.RecordLog(acct.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 个人开户（主体 %s），一次性赠送 quota %d", subject, grant.Quota))
	}
	return &VantageProvisionResult{
		Created:    created,
		Account:    newVantageAccountView(acct),
		Credential: newVantageCredentialView(cred, acct),
		TokenKey:   vantageTokenKeyForDelivery(cred, token),
	}, nil
}

// VantageProvisionEnterpriseAccount 确保企业账户：初始额度为零，
// 不参加个人注册赠送；重试不覆盖已有账户、充值或状态。
func VantageProvisionEnterpriseAccount(tenant, displayName string) (*VantageProvisionResult, error) {
	if err := validateVantageRef("tenant", tenant); err != nil {
		return nil, err
	}
	if err := validateVantageDisplayName(displayName); err != nil {
		return nil, err
	}
	acct, created, err := model.VantageEnsureEnterpriseAccount(tenant, displayName)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if created {
		model.RecordLog(acct.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 企业开户（租户 %s），初始额度 0", tenant))
	}
	return &VantageProvisionResult{
		Created: created,
		Account: newVantageAccountView(acct),
	}, nil
}

// VantageProvisionMemberCredential 为当前成员关系确保一个员工 Key。
// 员工创建和凭据轮换从不触发赠送；关系已撤销时拒绝。
func VantageProvisionMemberCredential(tenant, subject, membershipRef string) (*VantageProvisionResult, error) {
	if err := validateVantageRef("tenant", tenant); err != nil {
		return nil, err
	}
	if err := validateVantageRef("subject", subject); err != nil {
		return nil, err
	}
	if err := validateVantageRef("membership_ref", membershipRef); err != nil {
		return nil, err
	}
	acct, cred, token, created, err := model.VantageEnsureMemberCredential(tenant, subject, membershipRef)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if created {
		model.RecordLog(acct.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 员工凭据创建（租户 %s，主体 %s，成员关系 %s）", tenant, subject, membershipRef))
	}
	return &VantageProvisionResult{
		Created:    created,
		Account:    newVantageAccountView(acct),
		Credential: newVantageCredentialView(cred, acct),
		TokenKey:   vantageTokenKeyForDelivery(cred, token),
	}, nil
}

func vantageRotationIntentDigest(credentialRef string, expectedGeneration int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", credentialRef, expectedGeneration)))
	return hex.EncodeToString(digest[:])
}

// VantageRotateCredential 幂等轮换凭据。响应丢失后用同一操作标识
// 恢复同一次操作结果；旧代次预期与当前不符时返回冲突。
func VantageRotateCredential(credentialRef, opId string, expectedGeneration int) (*VantageProvisionResult, error) {
	if err := validateVantageRef("credential_ref", credentialRef); err != nil {
		return nil, err
	}
	if err := validateVantageRef("idempotency_key", opId); err != nil {
		return nil, err
	}
	if expectedGeneration < 0 {
		return nil, vantageInvalidRequest("expected_generation must not be negative")
	}
	outcome, err := model.VantageRotateCredential(credentialRef, opId, vantageRotationIntentDigest(credentialRef, expectedGeneration), expectedGeneration)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	account, err := model.GetVantageAccountBindingById(outcome.NewCredential.AccountBindingId)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if outcome.Created {
		model.RecordLog(account.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 凭据轮换（凭据 %s，第 %d 代）", credentialRef, outcome.NewCredential.Generation))
	}
	return &VantageProvisionResult{
		Created:    outcome.Created,
		Account:    newVantageAccountView(account),
		Credential: newVantageCredentialView(outcome.NewCredential, account),
		TokenKey:   vantageTokenKeyForDelivery(outcome.NewCredential, outcome.NewToken),
	}, nil
}

// VantageRevokeCredential 幂等撤销凭据，重复请求返回当前状态。
func VantageRevokeCredential(credentialRef string) (*VantageCredentialView, error) {
	if err := validateVantageRef("credential_ref", credentialRef); err != nil {
		return nil, err
	}
	cred, _, changed, err := model.VantageRevokeCredential(credentialRef)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	account, err := model.GetVantageAccountBindingById(cred.AccountBindingId)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if changed {
		model.RecordLog(account.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 凭据撤销（凭据 %s）", credentialRef))
	}
	return newVantageCredentialView(cred, account), nil
}

// VantageRevokeMembership 撤销整段成员关系；即使 Key 尚未创建，
// 也持久保存撤销事实，旧请求晚到不能成为当前系统凭据。
func VantageRevokeMembership(tenant, subject, membershipRef string) (*VantageCredentialView, error) {
	if err := validateVantageRef("tenant", tenant); err != nil {
		return nil, err
	}
	if err := validateVantageRef("subject", subject); err != nil {
		return nil, err
	}
	if err := validateVantageRef("membership_ref", membershipRef); err != nil {
		return nil, err
	}
	cred, _, changed, err := model.VantageRevokeMembership(tenant, subject, membershipRef)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	account, err := model.GetVantageAccountBindingById(cred.AccountBindingId)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	if changed {
		model.RecordLog(account.UserId, model.LogTypeSystem,
			fmt.Sprintf("Vantage 成员关系撤销（租户 %s，主体 %s，成员关系 %s）", tenant, subject, membershipRef))
	}
	return newVantageCredentialView(cred, account), nil
}

// VantageGetAccount 按 "personal/{subject}" 或 "enterprise/{tenant}" 引用
// 查询受管账户及关联凭据状态，不返回秘密。
func VantageGetAccount(accountRef string) (*VantageAccountView, []VantageCredentialView, error) {
	accountType, externalRef, ok := strings.Cut(accountRef, "/")
	if !ok || (accountType != model.VantageAccountTypePersonal && accountType != model.VantageAccountTypeEnterprise) {
		return nil, nil, vantageInvalidRequest("account_ref must be personal/{subject} or enterprise/{tenant}")
	}
	if err := validateVantageRef("external_ref", externalRef); err != nil {
		return nil, nil, err
	}
	acct, err := model.GetVantageAccountBinding(accountType, externalRef)
	if err != nil {
		return nil, nil, vantageErrorFromModel(err)
	}
	bindings, err := model.GetVantageCredentialBindingsByAccount(acct.Id)
	if err != nil {
		return nil, nil, vantageErrorFromModel(err)
	}
	views := make([]VantageCredentialView, 0, len(bindings))
	for i := range bindings {
		views = append(views, *newVantageCredentialView(&bindings[i], acct))
	}
	return newVantageAccountView(acct), views, nil
}

// VantageGetCredential 查询凭据状态、代次和归属，不返回秘密。
func VantageGetCredential(credentialRef string) (*VantageCredentialView, error) {
	if err := validateVantageRef("credential_ref", credentialRef); err != nil {
		return nil, err
	}
	cred, err := model.GetVantageCredentialBinding(credentialRef)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	account, err := model.GetVantageAccountBindingById(cred.AccountBindingId)
	if err != nil {
		return nil, vantageErrorFromModel(err)
	}
	view := newVantageCredentialView(cred, account)
	if view.Status == model.VantageCredentialStatusActive {
		active, err := model.VantageCredentialAuthenticationActive(cred, account.UserId)
		if err != nil {
			return nil, vantageErrorFromModel(err)
		}
		if !active || account.Status != model.VantageAccountStatusActive {
			view.Status = "disabled"
		}
	}
	return view, nil
}
