package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

// Gateway accounting is natively USD, independent of the mutable UI display
// currency. Every response identifies its conversion scale; operations retain it.
const VantageTokenQuotaCurrency = "USD"

var vantageQuotaAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)
var vantageQuotaOperationPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type VantageTokenQuotaView struct {
	TenantRef       string  `json:"tenant_ref"`
	CredentialRef   string  `json:"credential_ref"`
	Status          string  `json:"status"`
	Currency        string  `json:"currency"`
	QuotaPerUnit    string  `json:"quota_per_unit"`
	ConfigRevision  int64   `json:"config_revision"`
	LimitMode       string  `json:"limit_mode"`
	UnlimitedQuota  bool    `json:"unlimited_quota"`
	LimitAmount     *string `json:"limit_amount"`
	AccountedAmount string  `json:"accounted_amount"`
	SpentAmount     *string `json:"spent_amount"`
	ReservedAmount  *string `json:"reserved_amount"`
	AvailableAmount string  `json:"available_amount"`
	UsageState      string  `json:"usage_state"`
	UsageScope      string  `json:"usage_scope"`
	AdmissionStatus string  `json:"admission_status"`
	ResetAt         *string `json:"reset_at"`
}

type VantageQuotaSetup struct {
	ContractRevision string                 `json:"contract_revision"`
	Status           string                 `json:"status"`
	Quota            *VantageTokenQuotaView `json:"quota"`
}

type VantageQuotaFundsView struct {
	TenantRef       string `json:"tenant_ref"`
	Currency        string `json:"currency"`
	QuotaPerUnit    string `json:"quota_per_unit"`
	BalanceAmount   string `json:"balance_amount"`
	AvailableAmount string `json:"available_amount"`
	Status          string `json:"status"`
	Scope           string `json:"scope"`
}

type VantageTokenQuotaAdjustment struct {
	OperationId      string `json:"operation_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	Currency         string `json:"currency"`
	LimitAmount      string `json:"limit_amount"`
	ActorRef         string `json:"actor_ref"`
}

type VantageQuotaPageQuery struct {
	Cursor        string
	PageSize      int
	CredentialRef string
	Model         string
	StartTime     string
	EndTime       string
}

type VantageQuotaUsageView struct {
	TenantRef           string  `json:"tenant_ref"`
	CredentialRef       *string `json:"credential_ref"`
	RecordRef           string  `json:"record_ref"`
	Model               string  `json:"model"`
	OccurredAt          string  `json:"occurred_at"`
	Currency            string  `json:"currency"`
	QuotaPerUnit        string  `json:"quota_per_unit"`
	Amount              *string `json:"amount"`
	Kind                string  `json:"kind"`
	State               string  `json:"state"`
	RequestRef          string  `json:"request_ref"`
	BillingOperationRef *string `json:"billing_operation_ref"`
	AttemptRef          *string `json:"attempt_ref"`
}

func vantageQuotaScale() (decimal.Decimal, error) {
	if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
		return decimal.Zero, vantageErrorf(503, "configuration_unavailable", "native monetary scale is unavailable")
	}
	return decimal.NewFromFloat(common.QuotaPerUnit), nil
}

func vantageQuotaMoney(quota int64, scale decimal.Decimal) string {
	return decimal.NewFromInt(quota).Div(scale).StringFixed(6)
}

func vantageQuotaError(err error) error {
	var ve *VantageError
	if errors.As(err, &ve) {
		return err
	}
	switch {
	case errors.Is(err, model.ErrVantageQuotaOperationConflict):
		return vantageErrorf(409, "idempotency_conflict", "operation conflicts with a different request")
	case errors.Is(err, model.ErrVantageQuotaOperationNotFound):
		return vantageErrorf(404, "not_found", "unknown quota operation")
	case errors.Is(err, model.ErrVantageQuotaInvalidState):
		return vantageErrorf(503, "configuration_unavailable", "native quota policy is unavailable")
	default:
		return vantageErrorFromModel(err)
	}
}

func newVantageTokenQuotaView(snapshot *model.VantageTokenQuotaSnapshot, scale decimal.Decimal) *VantageTokenQuotaView {
	token := snapshot.Token
	view := &VantageTokenQuotaView{TenantRef: snapshot.Account.ExternalRef, CredentialRef: snapshot.Credential.CredentialRef, Status: "ready", Currency: VantageTokenQuotaCurrency, QuotaPerUnit: scale.String(), ConfigRevision: snapshot.Revision, LimitMode: "cumulative", UnlimitedQuota: token.UnlimitedQuota, AccountedAmount: vantageQuotaMoney(token.UsedQuota, scale), UsageState: "unknown", UsageScope: "native_token_accounting_including_preconsume", AdmissionStatus: "available"}
	// Unknown final spending and reservation splits must remain null. We do not
	// invent a reservation ledger or sum partial logs to fill these values.
	available := max(int64(0), snapshot.User.Quota)
	if token.UnlimitedQuota {
		view.Status = "not_ready"
	} else {
		total := decimal.NewFromInt(token.UsedQuota).Add(decimal.NewFromInt(token.RemainQuota))
		limit := total.Div(scale).StringFixed(6)
		view.LimitAmount = &limit
		available = max(int64(0), min(token.RemainQuota, snapshot.User.Quota))
	}
	switch {
	case snapshot.Account.Status != model.VantageAccountStatusActive || snapshot.User.Status != common.UserStatusEnabled:
		view.AdmissionStatus = "account_not_active"
	case token.DeletedAt.Valid || token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted && token.Status != common.TokenStatusExpired:
		view.AdmissionStatus = "credential_not_active"
	case token.Status == common.TokenStatusExpired || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp():
		view.AdmissionStatus = "credential_expired"
	case !token.UnlimitedQuota && (token.RemainQuota <= 0 || token.Status == common.TokenStatusExhausted):
		view.AdmissionStatus = "insufficient_token_quota"
	case snapshot.User.Quota <= 0:
		view.AdmissionStatus = "insufficient_user_quota"
	}
	if view.AdmissionStatus != "available" {
		available = 0
	}
	view.AvailableAmount = vantageQuotaMoney(available, scale)
	return view
}

func VantageGetTokenQuota(tenant, credentialRef string) (*VantageTokenQuotaView, error) {
	if err := validateVantageRef("tenant_ref", tenant); err != nil {
		return nil, err
	}
	if err := validateVantageRef("credential_ref", credentialRef); err != nil {
		return nil, err
	}
	scale, err := vantageQuotaScale()
	if err != nil {
		return nil, err
	}
	snapshot, err := model.GetVantageTokenQuota(tenant, credentialRef)
	if err != nil {
		return nil, vantageQuotaError(err)
	}
	return newVantageTokenQuotaView(snapshot, scale), nil
}

func VantageGetQuotaFunds(tenant string) (*VantageQuotaFundsView, error) {
	if err := validateVantageRef("tenant_ref", tenant); err != nil {
		return nil, err
	}
	scale, err := vantageQuotaScale()
	if err != nil {
		return nil, err
	}
	account, user, err := model.GetVantageQuotaFunds(tenant)
	if err != nil {
		return nil, vantageQuotaError(err)
	}
	available := max(int64(0), user.Quota)
	status := "active"
	if account.Status != model.VantageAccountStatusActive || user.Status != common.UserStatusEnabled {
		status, available = "disabled", 0
	}
	return &VantageQuotaFundsView{TenantRef: tenant, Currency: VantageTokenQuotaCurrency, QuotaPerUnit: scale.String(), BalanceAmount: vantageQuotaMoney(user.Quota, scale), AvailableAmount: vantageQuotaMoney(available, scale), Status: status, Scope: "native_user_wallet_snapshot"}, nil
}

func VantageSetTokenQuota(tenant, credentialRef, idempotencyKey string, request VantageTokenQuotaAdjustment) (*model.VantageTokenQuotaOperation, error) {
	for _, field := range []struct{ name, value string }{{"tenant_ref", tenant}, {"credential_ref", credentialRef}, {"actor_ref", request.ActorRef}} {
		if err := validateVantageRef(field.name, field.value); err != nil {
			return nil, err
		}
	}
	if !vantageQuotaOperationPattern.MatchString(request.OperationId) || idempotencyKey != request.OperationId {
		return nil, vantageInvalidRequest("operation_id must be a UUID equal to Idempotency-Key")
	}
	request.OperationId = strings.ToLower(request.OperationId)
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 1 || *request.ExpectedRevision >= common.MaxWalletQuota {
		return nil, vantageInvalidRequest("expected_revision must be a positive safe integer")
	}
	if request.Currency != VantageTokenQuotaCurrency || !vantageQuotaAmountPattern.MatchString(request.LimitAmount) {
		return nil, vantageErrorf(422, "amount_invalid", "limit_amount must be a non-negative USD decimal string with at most six fractional digits")
	}
	amount, err := decimal.NewFromString(request.LimitAmount)
	if err != nil {
		return nil, vantageErrorf(422, "amount_invalid", "invalid limit amount")
	}
	request.LimitAmount = amount.StringFixed(6)
	encoded, err := common.Marshal([]any{tenant, credentialRef, request})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	intent := hex.EncodeToString(digest[:])
	// Recover before consulting mutable conversion configuration or stale revision.
	stored, lookup := model.GetVantageTokenQuotaOperation(tenant, request.OperationId)
	if lookup == nil {
		if stored.RequestDigest != intent {
			return nil, vantageQuotaError(model.ErrVantageQuotaOperationConflict)
		}
		return stored, nil
	}
	if !errors.Is(lookup, model.ErrVantageQuotaOperationNotFound) {
		return nil, vantageQuotaError(lookup)
	}
	scale, err := vantageQuotaScale()
	if err != nil {
		return nil, err
	}
	native := amount.Mul(scale)
	if !native.Equal(native.Truncate(0)) {
		return nil, vantageErrorf(422, "amount_invalid", "amount is not exactly representable in native quota units")
	}
	quota, err := common.WalletQuotaFromDecimalStrict(native)
	if err != nil || quota > common.MaxTokenQuota {
		return nil, vantageErrorf(422, "amount_invalid", "amount exceeds the native token quota range")
	}
	operation := model.VantageTokenQuotaOperation{OperationId: request.OperationId, ActorRef: request.ActorRef, RequestDigest: intent, ExpectedRevision: *request.ExpectedRevision, Currency: request.Currency, LimitAmount: request.LimitAmount, QuotaPerUnit: scale.String()}
	outcome, err := model.SetVantageTokenQuota(tenant, credentialRef, quota, operation)
	if err != nil {
		return nil, vantageQuotaError(err)
	}
	return outcome, nil
}

func VantageGetTokenQuotaOperation(tenant, operationID string) (*model.VantageTokenQuotaOperation, error) {
	if err := validateVantageRef("tenant_ref", tenant); err != nil {
		return nil, err
	}
	if !vantageQuotaOperationPattern.MatchString(operationID) {
		return nil, vantageInvalidRequest("operation_id must be a UUID")
	}
	operation, err := model.GetVantageTokenQuotaOperation(tenant, strings.ToLower(operationID))
	if err != nil {
		return nil, vantageQuotaError(err)
	}
	return operation, nil
}

// Cursor integrity uses the existing server HMAC secret. It grants no access;
// every page still re-authenticates and resolves the current native account.
func vantageQuotaPage(query VantageQuotaPageQuery, scope string) (int64, int64, int, error) {
	size := query.PageSize
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 100 {
		return 0, 0, 0, vantageInvalidRequest("page_size must be between 1 and 100")
	}
	if query.Cursor == "" {
		return 0, 0, size, nil
	}
	if len(query.Cursor) > 512 {
		return 0, 0, 0, vantageInvalidRequest("invalid cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return 0, 0, 0, vantageInvalidRequest("invalid cursor")
	}
	fields := strings.Split(string(decoded), ":")
	if len(fields) != 3 {
		return 0, 0, 0, vantageInvalidRequest("invalid cursor")
	}
	expected := common.GenerateHMAC("vantage-quota:" + scope + ":" + fields[0] + ":" + fields[1])
	if !hmac.Equal([]byte(fields[2]), []byte(expected)) {
		return 0, 0, 0, vantageInvalidRequest("cursor does not match this query")
	}
	after, e1 := strconv.ParseInt(fields[0], 10, 64)
	high, e2 := strconv.ParseInt(fields[1], 10, 64)
	if e1 != nil || e2 != nil || after < 0 || high < after {
		return 0, 0, 0, vantageInvalidRequest("invalid cursor")
	}
	return after, high, size, nil
}

func vantageQuotaNextCursor(after, high int64, scope string) string {
	data := fmt.Sprintf("%d:%d", after, high)
	signature := common.GenerateHMAC("vantage-quota:" + scope + ":" + data)
	return base64.RawURLEncoding.EncodeToString([]byte(data + ":" + signature))
}

func VantageListTokenQuotas(tenant string, query VantageQuotaPageQuery) ([]VantageTokenQuotaView, *string, error) {
	if err := validateVantageRef("tenant_ref", tenant); err != nil {
		return nil, nil, err
	}
	scale, err := vantageQuotaScale()
	if err != nil {
		return nil, nil, err
	}
	scope := "tokens|" + tenant
	after, high, size, err := vantageQuotaPage(query, scope)
	if err != nil {
		return nil, nil, err
	}
	if after > math.MaxInt || high > math.MaxInt {
		return nil, nil, vantageInvalidRequest("invalid cursor range")
	}
	rows, maximum, err := model.ListVantageTokenQuotas(tenant, int(after), int(high), size+1)
	if err != nil {
		return nil, nil, vantageQuotaError(err)
	}
	var next *string
	if len(rows) > size {
		cursor := vantageQuotaNextCursor(int64(rows[size-1].Credential.Id), int64(maximum), scope)
		next = &cursor
		rows = rows[:size]
	}
	items := make([]VantageTokenQuotaView, 0, len(rows))
	for _, row := range rows {
		view := newVantageTokenQuotaView(&row, scale)
		items = append(items, *view)
	}
	return items, next, nil
}

func VantageListQuotaUsage(tenant string, query VantageQuotaPageQuery) ([]VantageQuotaUsageView, *string, error) {
	if err := validateVantageRef("tenant_ref", tenant); err != nil {
		return nil, nil, err
	}
	if len(query.Model) > 256 {
		return nil, nil, vantageInvalidRequest("model filter is too long")
	}
	scale, err := vantageQuotaScale()
	if err != nil {
		return nil, nil, err
	}
	account, err := model.GetVantageAccountBinding(model.VantageAccountTypeEnterprise, tenant)
	if err != nil {
		return nil, nil, vantageQuotaError(err)
	}
	native := model.VantageQuotaUsageQuery{UserId: account.UserId, ModelName: query.Model}
	if query.CredentialRef != "" {
		if err := validateVantageRef("credential_ref", query.CredentialRef); err != nil {
			return nil, nil, err
		}
		tokenID, err := model.GetVantageQuotaUsageTokenID(account, query.CredentialRef)
		if err != nil {
			return nil, nil, vantageQuotaError(err)
		}
		native.TokenId = tokenID
	}
	for _, field := range []struct {
		input  string
		target *int64
	}{{query.StartTime, &native.StartTime}, {query.EndTime, &native.EndTime}} {
		if field.input == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, field.input)
		if err != nil || parsed.Unix() <= 0 || parsed.Nanosecond() != 0 {
			return nil, nil, vantageInvalidRequest("time filters require RFC3339 whole seconds after the Unix epoch")
		}
		*field.target = parsed.Unix()
	}
	if native.EndTime > 0 && native.StartTime >= native.EndTime {
		return nil, nil, vantageInvalidRequest("start_time must precede end_time")
	}
	encoded, _ := common.Marshal([]string{"usage", tenant, query.CredentialRef, query.Model, query.StartTime, query.EndTime})
	scope := string(encoded)
	after, high, size, err := vantageQuotaPage(query, scope)
	if err != nil {
		return nil, nil, err
	}
	native.After, native.High, native.Size = after, high, size+1
	rows, maximum, err := model.ListVantageQuotaUsage(native)
	if err != nil {
		return nil, nil, &VantageError{Status: 503, Code: "usage_unavailable", Message: "native usage records are unavailable", Err: err}
	}
	var next *string
	if len(rows) > size {
		cursor := vantageQuotaNextCursor(int64(rows[size-1].Id), maximum, scope)
		next = &cursor
		rows = rows[:size]
	}
	tokenIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.TokenId > 0 {
			tokenIDs = append(tokenIDs, row.TokenId)
		}
	}
	refs := make(map[int]string, len(tokenIDs))
	if len(tokenIDs) > 0 {
		var bindings []model.VantageCredentialBinding
		if err := model.DB.Select("token_id", "credential_ref").Where("namespace = ? AND account_binding_id = ? AND token_id IN ?", model.VantageIntegrationNamespace, account.Id, tokenIDs).Find(&bindings).Error; err != nil {
			return nil, nil, vantageQuotaError(err)
		}
		for _, binding := range bindings {
			if binding.TokenId != nil {
				refs[*binding.TokenId] = binding.CredentialRef
			}
		}
	}
	items := make([]VantageQuotaUsageView, 0, len(rows))
	for _, row := range rows {
		item := VantageQuotaUsageView{TenantRef: tenant, RecordRef: "log-" + strconv.Itoa(row.Id), Model: row.ModelName, OccurredAt: time.Unix(row.CreatedAt, 0).UTC().Format(time.RFC3339), Currency: VantageTokenQuotaCurrency, QuotaPerUnit: scale.String(), Kind: "consume", State: "recorded", RequestRef: row.RequestId, BillingOperationRef: row.BillingOperationId}
		if ref, ok := refs[row.TokenId]; ok {
			item.CredentialRef = &ref
		}
		if row.Type == model.LogTypeRefund {
			item.Kind = "refund"
		}
		if row.Quota >= 0 {
			quota := int64(row.Quota)
			if row.Type == model.LogTypeRefund {
				quota = -quota
			}
			value := vantageQuotaMoney(quota, scale)
			item.Amount = &value
		} else {
			item.State = "unknown"
		}
		items = append(items, item)
	}
	return items, next, nil
}

func VantageProvisionTokenQuota(result *VantageProvisionResult) (*VantageQuotaSetup, error) {
	if result.Account.Type != model.VantageAccountTypeEnterprise || result.Credential == nil {
		return nil, nil
	}
	tenant := strings.TrimPrefix(result.Account.Ref, model.VantageAccountTypeEnterprise+"/")
	quota, err := VantageGetTokenQuota(tenant, result.Credential.Ref)
	if err != nil {
		return nil, err
	}
	return &VantageQuotaSetup{ContractRevision: model.VantageTokenQuotaContract, Status: quota.Status, Quota: quota}, nil
}
