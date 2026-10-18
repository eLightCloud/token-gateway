package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func vantageRespondTokenQuota(c *gin.Context, name string, value any, next *string, paged bool) {
	response := gin.H{"success": true, "contract_revision": model.VantageTokenQuotaContract, "source": "lighting", "observed_at": time.Now().UTC().Format(time.RFC3339), "request_id": vantageRequestId(c), name: value}
	if paged {
		response["next_cursor"] = next
		response["consistency"] = "bounded_ids_live_values"
	}
	if name == "usage" {
		response["completeness"] = "recorded_logs_only"
		response["consumption_logging_enabled"] = common.LogConsumeEnabled
	}
	c.JSON(http.StatusOK, response)
}

func VantageGetQuotaFunds(c *gin.Context) {
	funds, err := service.VantageGetQuotaFunds(c.Param("tenant"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondTokenQuota(c, "funds", funds, nil, false)
}

func VantageGetTokenQuota(c *gin.Context) {
	quota, err := service.VantageGetTokenQuota(c.Param("tenant"), c.Param("credentialRef"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondTokenQuota(c, "quota", quota, nil, false)
}

func VantagePutTokenQuota(c *gin.Context) {
	var request service.VantageTokenQuotaAdjustment
	if !vantageBindJSON(c, &request, map[string]bool{"operation_id": true, "expected_revision": true, "currency": true, "limit_amount": true, "actor_ref": true}) {
		return
	}
	operation, err := service.VantageSetTokenQuota(c.Param("tenant"), c.Param("credentialRef"), c.GetHeader("Idempotency-Key"), request)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	// Recorded rejection is a successful delivery, not a successful policy edit.
	vantageRespondTokenQuota(c, "operation", operation, nil, false)
}

func VantageGetTokenQuotaOperation(c *gin.Context) {
	operation, err := service.VantageGetTokenQuotaOperation(c.Param("tenant"), c.Param("operationID"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondTokenQuota(c, "operation", operation, nil, false)
}

func vantageTokenQuotaPage(c *gin.Context) (service.VantageQuotaPageQuery, bool) {
	query := service.VantageQuotaPageQuery{Cursor: c.Query("cursor"), CredentialRef: c.Query("credential_ref"), Model: c.Query("model"), StartTime: c.Query("start_time"), EndTime: c.Query("end_time")}
	if values, exists := c.Request.URL.Query()["page_size"]; exists {
		if len(values) != 1 {
			vantageRejectRequest(c, "page_size must be specified once")
			return query, false
		}
		size, err := strconv.Atoi(values[0])
		if err != nil || size < 1 || size > 100 {
			vantageRejectRequest(c, "page_size must be between 1 and 100")
			return query, false
		}
		query.PageSize = size
	}
	return query, true
}

func VantageListTokenQuotas(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	quotas, next, err := service.VantageListTokenQuotas(c.Param("tenant"), query)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondTokenQuota(c, "quotas", quotas, next, true)
}

func VantageListQuotaUsage(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	usage, next, err := service.VantageListQuotaUsage(c.Param("tenant"), query)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondTokenQuota(c, "usage", usage, next, true)
}

func VantageGetTokenQuotaContract(c *gin.Context) {
	c.Data(http.StatusOK, "application/vnd.oai.openapi+json", vantagequota.Contract)
}
