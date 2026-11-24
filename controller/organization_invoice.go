package controller

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

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
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	invoice, err := service.GetOrganizationInvoice(organizationId, period, refresh)
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

func exportOrganizationInvoice(c *gin.Context, organizationId int) {
	period, ok := organizationInvoicePeriodFromQuery(c)
	if !ok {
		return
	}
	invoice, err := service.GetOrganizationInvoice(organizationId, period, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if invoice.GenerationStatus != model.OrganizationInvoiceGenerationStatusReady {
		common.ApiErrorMsg(c, "organization invoice is still generating")
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
