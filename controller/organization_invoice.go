package controller

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type organizationSettlementRuleUpdateRequest struct {
	CategoryKey     string `json:"category_key"`
	Factor          string `json:"factor"`
	EffectiveMonth  string `json:"effective_month"`
	ExpectedVersion *int   `json:"expected_version"`
}

type organizationSettlementRuleUpdateResponse struct {
	CategoryKey          string `json:"category_key"`
	Factor               string `json:"factor"`
	FactorScaled         int    `json:"factor_scaled"`
	EffectiveMonth       string `json:"effective_month"`
	Version              int    `json:"version"`
	CreatedAt            int64  `json:"created_at"`
	UpdatedAt            int64  `json:"updated_at"`
	Changed              bool   `json:"changed"`
	PreviousFactor       string `json:"previous_factor"`
	PreviousFactorScaled int    `json:"previous_factor_scaled"`
}

func organizationInvoicePeriodFromQuery(c *gin.Context) (model.OrganizationInvoicePeriod, bool) {
	period, err := model.NewOrganizationInvoicePeriod(
		strings.TrimSpace(c.Query("start_date")),
		strings.TrimSpace(c.Query("end_date")),
		time.Now(),
	)
	if err != nil {
		common.ApiError(c, err)
		return model.OrganizationInvoicePeriod{}, false
	}
	return period, true
}

func organizationInvoiceEffectiveMonthFromQuery(c *gin.Context) (int, bool) {
	value := strings.TrimSpace(c.Query("effective_month"))
	if value == "" {
		value = time.Now().In(time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)).Format("2006-01")
	}
	month, err := model.ParseOrganizationInvoiceMonth(value)
	if err != nil {
		common.ApiError(c, err)
		return 0, false
	}
	return month, true
}

func currentManagedOrganizationId(c *gin.Context) (int, bool) {
	current, ok := requireCurrentOrganization(c)
	if !ok {
		return 0, false
	}
	if !requireOrganizationManager(c, current.Organization.Id) {
		return 0, false
	}
	return current.Organization.Id, true
}

func adminOrganizationId(c *gin.Context) (int, bool) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil || organizationId <= 0 {
		common.ApiErrorMsg(c, "invalid organization id")
		return 0, false
	}
	return organizationId, true
}

func getOrganizationInvoice(c *gin.Context, organizationId int) {
	period, ok := organizationInvoicePeriodFromQuery(c)
	if !ok {
		return
	}
	invoice, err := model.GetOrganizationInvoice(organizationId, period)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, invoice)
}

func GetCurrentOrganizationInvoice(c *gin.Context) {
	organizationId, ok := currentManagedOrganizationId(c)
	if !ok {
		return
	}
	getOrganizationInvoice(c, organizationId)
}

func AdminGetOrganizationInvoice(c *gin.Context) {
	organizationId, ok := adminOrganizationId(c)
	if !ok {
		return
	}
	getOrganizationInvoice(c, organizationId)
}

func getOrganizationSettlementRules(c *gin.Context, organizationId int) {
	effectiveMonth, ok := organizationInvoiceEffectiveMonthFromQuery(c)
	if !ok {
		return
	}
	rules, err := model.GetOrganizationSettlementRuleOptions(organizationId, effectiveMonth)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, rules)
}

func GetCurrentOrganizationSettlementRules(c *gin.Context) {
	organizationId, ok := currentManagedOrganizationId(c)
	if !ok {
		return
	}
	getOrganizationSettlementRules(c, organizationId)
}

func AdminGetOrganizationSettlementRules(c *gin.Context) {
	organizationId, ok := adminOrganizationId(c)
	if !ok {
		return
	}
	getOrganizationSettlementRules(c, organizationId)
}

func recordOrganizationSettlementRuleUpdateFailure(
	c *gin.Context,
	organizationId int,
	req organizationSettlementRuleUpdateRequest,
	err error,
	actualVersion *int,
) {
	params := map[string]interface{}{
		"organization_id": organizationId,
		"category_key":    req.CategoryKey,
		"effective_month": req.EffectiveMonth,
		"factor":          req.Factor,
		"error":           err.Error(),
	}
	if req.ExpectedVersion != nil {
		params["expected_version"] = *req.ExpectedVersion
	}
	if actualVersion != nil {
		params["actual_version"] = *actualVersion
	}
	recordManageAudit(c, "organization.settlement_rule_update_failed", params)
}

func updateOrganizationSettlementRule(c *gin.Context, organizationId int) {
	var req organizationSettlementRuleUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, nil)
		common.ApiError(c, err)
		return
	}
	req.CategoryKey = strings.TrimSpace(req.CategoryKey)
	if req.ExpectedVersion == nil {
		err := errors.New("expected_version is required")
		recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, nil)
		common.ApiError(c, err)
		return
	}
	effectiveMonth, err := model.ParseOrganizationInvoiceMonth(strings.TrimSpace(req.EffectiveMonth))
	if err != nil {
		recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, nil)
		common.ApiError(c, err)
		return
	}
	factorScaled, err := model.ParseOrganizationSettlementFactor(req.Factor)
	if err != nil {
		recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, nil)
		common.ApiError(c, err)
		return
	}
	result, err := model.UpdateOrganizationSettlementRule(
		organizationId,
		req.CategoryKey,
		effectiveMonth,
		factorScaled,
		*req.ExpectedVersion,
	)
	if err != nil {
		var conflict *model.OrganizationSettlementVersionConflictError
		if errors.As(err, &conflict) {
			recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, &conflict.Actual)
			c.JSON(http.StatusConflict, gin.H{
				"success": false,
				"message": err.Error(),
				"data": gin.H{
					"expected_version": conflict.Expected,
					"actual_version":   conflict.Actual,
				},
			})
			return
		}
		recordOrganizationSettlementRuleUpdateFailure(c, organizationId, req, err, nil)
		common.ApiError(c, err)
		return
	}
	if result.Changed {
		recordManageAudit(c, "organization.settlement_rule_update", map[string]interface{}{
			"organization_id":   organizationId,
			"category_key":      result.Rule.CategoryKey,
			"effective_month":   model.FormatOrganizationInvoiceMonth(result.Rule.EffectiveMonth),
			"from":              model.FormatOrganizationSettlementFactor(result.PreviousFactorScaled),
			"to":                model.FormatOrganizationSettlementFactor(result.Rule.FactorScaled),
			"expected_version":  *req.ExpectedVersion,
			"actual_version":    result.Rule.Version,
			"actual_updated_at": result.Rule.UpdatedAt,
		})
	}
	common.ApiSuccess(c, organizationSettlementRuleUpdateResponse{
		CategoryKey:          result.Rule.CategoryKey,
		Factor:               model.FormatOrganizationSettlementFactor(result.Rule.FactorScaled),
		FactorScaled:         result.Rule.FactorScaled,
		EffectiveMonth:       model.FormatOrganizationInvoiceMonth(result.Rule.EffectiveMonth),
		Version:              result.Rule.Version,
		CreatedAt:            result.Rule.CreatedAt,
		UpdatedAt:            result.Rule.UpdatedAt,
		Changed:              result.Changed,
		PreviousFactor:       model.FormatOrganizationSettlementFactor(result.PreviousFactorScaled),
		PreviousFactorScaled: result.PreviousFactorScaled,
	})
}

func UpdateCurrentOrganizationSettlementRule(c *gin.Context) {
	organizationId, ok := currentManagedOrganizationId(c)
	if !ok {
		return
	}
	updateOrganizationSettlementRule(c, organizationId)
}

func AdminUpdateOrganizationSettlementRule(c *gin.Context) {
	organizationId, ok := adminOrganizationId(c)
	if !ok {
		return
	}
	updateOrganizationSettlementRule(c, organizationId)
}

func invoiceCSVAmount(value string) (string, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil {
		return "", fmt.Errorf("invalid organization invoice amount %q: %w", value, err)
	}
	return amount.StringFixed(6), nil
}

type organizationInvoiceCSVAmountFormatter struct {
	err error
}

func (f *organizationInvoiceCSVAmountFormatter) amount(value string) string {
	if f.err != nil {
		return ""
	}
	formatted, err := invoiceCSVAmount(value)
	if err != nil {
		f.err = err
		return ""
	}
	return formatted
}

func invoiceCSVFactor(row model.OrganizationInvoiceCategoryRow) string {
	if !row.MultipleFactors {
		return row.Factor
	}
	parts := make([]string, 0, len(row.FactorSegments))
	for _, segment := range row.FactorSegments {
		parts = append(parts, segment.PeriodMonth+":"+segment.Factor)
	}
	return strings.Join(parts, "; ")
}

func invoiceCSVRuleDetails(row model.OrganizationInvoiceCategoryRow) string {
	parts := make([]string, 0, len(row.FactorSegments))
	for _, segment := range row.FactorSegments {
		sourceMonth := segment.RuleEffectiveMonth
		if sourceMonth == "" {
			sourceMonth = "default"
		}
		parts = append(parts, fmt.Sprintf(
			"%s:%s@%s(v%d)",
			segment.PeriodMonth,
			segment.Factor,
			sourceMonth,
			segment.RuleVersion,
		))
	}
	return strings.Join(parts, "; ")
}

func writeOrganizationInvoiceCSV(writer *csv.Writer, organizationId int, invoice *model.OrganizationInvoice) error {
	amountFormatter := organizationInvoiceCSVAmountFormatter{}
	_ = writer.Write([]string{"组织 ID", strconv.Itoa(organizationId)})
	_ = writer.Write([]string{"账期", invoice.Period.StartDate + " ~ " + invoice.Period.EndDate})
	_ = writer.Write([]string{"时区", invoice.Period.Timezone})
	_ = writer.Write([]string{"开始时间戳", strconv.FormatInt(invoice.Period.StartTimestamp, 10)})
	_ = writer.Write([]string{"结束时间戳", strconv.FormatInt(invoice.Period.EndTimestamp, 10)})
	_ = writer.Write([]string{"币种", invoice.Currency})
	_ = writer.Write([]string{})

	categoryHeader := []string{"模型类别"}
	for _, account := range invoice.Accounts {
		categoryHeader = append(categoryHeader, account.Username)
	}
	categoryHeader = append(categoryHeader, "折前合计", "结算系数", "规则明细", "结算后金额", "折前额度(quota)")
	_ = writer.Write([]string{"# 模型归类结算汇总"})
	_ = writer.Write(categoryHeader)
	for _, row := range invoice.CategoryRows {
		record := []string{row.CategoryName}
		for _, amount := range row.AccountAmounts {
			record = append(record, amountFormatter.amount(amount.GrossAmountUSD))
		}
		record = append(
			record,
			amountFormatter.amount(row.GrossAmountUSD),
			invoiceCSVFactor(row),
			invoiceCSVRuleDetails(row),
			amountFormatter.amount(row.SettledAmountUSD),
			strconv.FormatInt(row.GrossQuota, 10),
		)
		_ = writer.Write(record)
	}
	categoryTotal := []string{"合计"}
	for _, account := range invoice.Accounts {
		categoryTotal = append(categoryTotal, amountFormatter.amount(account.GrossAmountUSD))
	}
	categoryTotal = append(
		categoryTotal,
		amountFormatter.amount(invoice.GrossTotalAmountUSD),
		"—",
		"—",
		amountFormatter.amount(invoice.SettledTotalAmountUSD),
		strconv.FormatInt(invoice.GrossTotalQuota, 10),
	)
	_ = writer.Write(categoryTotal)
	_ = writer.Write([]string{})

	modelHeader := []string{"模型"}
	for _, account := range invoice.Accounts {
		modelHeader = append(modelHeader, account.Username)
	}
	modelHeader = append(modelHeader, "合计", "占比", "折前额度(quota)")
	_ = writer.Write([]string{"# AI 模型消费汇总"})
	_ = writer.Write(modelHeader)
	for _, row := range invoice.ModelRows {
		record := []string{row.ModelName}
		for _, amount := range row.AccountAmounts {
			record = append(record, amountFormatter.amount(amount.GrossAmountUSD))
		}
		record = append(
			record,
			amountFormatter.amount(row.GrossAmountUSD),
			row.SharePercent+"%",
			strconv.FormatInt(row.GrossQuota, 10),
		)
		_ = writer.Write(record)
	}
	modelTotal := []string{"合计"}
	for _, account := range invoice.Accounts {
		modelTotal = append(modelTotal, amountFormatter.amount(account.GrossAmountUSD))
	}
	modelTotal = append(
		modelTotal,
		amountFormatter.amount(invoice.GrossTotalAmountUSD),
		"100.0%",
		strconv.FormatInt(invoice.GrossTotalQuota, 10),
	)
	_ = writer.Write(modelTotal)
	return amountFormatter.err
}

func exportOrganizationInvoice(c *gin.Context, organizationId int) {
	period, ok := organizationInvoicePeriodFromQuery(c)
	if !ok {
		return
	}
	invoice, err := model.GetOrganizationInvoice(organizationId, period)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&buffer)
	if err := writeOrganizationInvoiceCSV(writer, organizationId, invoice); err != nil {
		common.ApiError(c, err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		common.ApiError(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header(
		"Content-Disposition",
		fmt.Sprintf(
			"attachment; filename=\"organization-%d-invoice-%s-%s.csv\"",
			organizationId,
			invoice.Period.StartDate,
			invoice.Period.EndDate,
		),
	)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buffer.Bytes())
}

func ExportCurrentOrganizationInvoice(c *gin.Context) {
	organizationId, ok := currentManagedOrganizationId(c)
	if !ok {
		return
	}
	exportOrganizationInvoice(c, organizationId)
}

func AdminExportOrganizationInvoice(c *gin.Context) {
	organizationId, ok := adminOrganizationId(c)
	if !ok {
		return
	}
	exportOrganizationInvoice(c, organizationId)
}
