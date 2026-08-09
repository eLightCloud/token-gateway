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
	exportContext, err := model.GetOrganizationInvoiceExportContext(organizationId, invoice.Accounts)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var buffer bytes.Buffer
	buffer.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&buffer)
	if err := writeOrganizationInvoiceCSV(writer, exportContext, invoice); err != nil {
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
