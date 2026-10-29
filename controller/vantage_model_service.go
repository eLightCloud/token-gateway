package controller

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/vantagequota"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func vantageServiceResponse(c *gin.Context, fields gin.H, err error) {
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	if c.Param("tenant") != "" || c.Param("subject") != "" {
		kind, ref := vantageServiceAccount(c)
		fields["account_ref"] = kind + "/" + ref
	}
	fields["success"] = true
	fields["contract_revision"] = "model-service-2"
	fields["source"] = "lighting"
	fields["observed_at"] = time.Now().UTC().Format(time.RFC3339)
	fields["request_id"] = vantageRequestId(c)
	c.JSON(http.StatusOK, fields)
}

func vantageServiceAccount(c *gin.Context) (string, string) {
	if c.Param("tenant") != "" {
		return model.VantageAccountTypeEnterprise, c.Param("tenant")
	}
	return model.VantageAccountTypePersonal, c.Param("subject")
}

func VantageModelServiceContract(c *gin.Context) {
	c.Data(http.StatusOK, "application/vnd.oai.openapi+json", vantagequota.ModelServiceContract)
}
func VantageModelServiceSettings(c *gin.Context) {
	value, err := service.VantageMoneySettings()
	vantageServiceResponse(c, gin.H{"settings": value}, err)
}
func VantageModelServiceFunds(c *gin.Context) {
	kind, ref := vantageServiceAccount(c)
	value, err := service.VantageServiceFunds(kind, ref)
	vantageServiceResponse(c, gin.H{"funds": value}, err)
}
func VantageModelServicePlans(c *gin.Context) {
	value, err := service.VantageServicePlans()
	vantageServiceResponse(c, gin.H{"items": value}, err)
}
func VantageModelServiceQuota(c *gin.Context) {
	value, err := service.VantageServiceQuota(c.Param("tenant"), c.Param("credentialRef"))
	vantageServiceResponse(c, gin.H{"quota": value}, err)
}
func VantageModelServiceOperation(c *gin.Context) {
	kind, ref := vantageServiceAccount(c)
	value, err := service.VantageServiceOperation(kind, ref, c.Param("operationID"))
	vantageServiceResponse(c, gin.H{"operation": value}, err)
}
func VantageModelServiceUsage(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	kind, ref := vantageServiceAccount(c)
	items, next, err := service.VantageServiceUsage(kind, ref, query)
	vantageServiceResponse(c, gin.H{"items": items, "next_cursor": next, "consumption_logging_enabled": common.LogConsumeEnabled}, err)
}
func VantageModelServiceRecharges(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	kind, ref := vantageServiceAccount(c)
	items, next, err := service.VantageServiceRecharges(kind, ref, query)
	vantageServiceResponse(c, gin.H{"items": items, "next_cursor": next}, err)
}
func VantageModelServiceSubscriptions(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	subs, records, next, err := service.VantageServiceSubscriptions(c.Param("subject"), query)
	vantageServiceResponse(c, gin.H{"subscriptions": subs, "purchases": records, "next_cursor": next}, err)
}
func VantageModelServiceQuotas(c *gin.Context) {
	query, ok := vantageTokenQuotaPage(c)
	if !ok {
		return
	}
	// Existing native Key paging is bounded and retains historical ownership.
	rows, next, err := service.VantageListTokenQuotas(c.Param("tenant"), query)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	items := []*vantagequota.Quota{}
	for _, row := range rows {
		item, err := service.VantageServiceQuota(c.Param("tenant"), row.CredentialRef)
		if err != nil {
			vantageRespondError(c, err)
			return
		}
		items = append(items, item)
	}
	vantageServiceResponse(c, gin.H{"items": items, "next_cursor": next}, nil)
}
func VantageModelServicePurchase(c *gin.Context) {
	var input vantagequota.PurchaseInput
	if !vantageBindJSON(c, &input, map[string]bool{"operation_id": true, "plan_id": true, "quote_revision": true, "actor_ref": true}) {
		return
	}
	result, err := service.VantageRecoverSubscriptionPurchase(c.Param("subject"), c.GetHeader("Idempotency-Key"), input)
	vantageServiceResponse(c, gin.H{"operation": result}, err)
}

func VantageModelServiceAssignment(c *gin.Context) {
	var input vantagequota.AssignmentInput
	if !vantageBindJSON(c, &input, map[string]bool{"operation_id": true, "action": true, "plan_id": true, "plan_revision": true, "expected_revision": true, "ends_at": true, "actor_ref": true}) {
		return
	}
	result, err := service.VantageServiceAssignment(c.Param("subject"), c.GetHeader("Idempotency-Key"), input)
	vantageServiceResponse(c, gin.H{"operation": result}, err)
}
func VantageModelServiceSetQuota(c *gin.Context) {
	var input vantagequota.QuotaInput
	if !vantageBindJSON(c, &input, map[string]bool{"operation_id": true, "expected_revision": true, "currency": true, "mode": true, "remaining_amount": true, "actor_ref": true}) {
		return
	}
	result, err := service.VantageServiceSetQuota(c.Param("tenant"), c.Param("credentialRef"), c.GetHeader("Idempotency-Key"), input)
	vantageServiceResponse(c, gin.H{"operation": result}, err)
}
func VantageModelServiceMember(c *gin.Context) {
	var body struct {
		MembershipRef string `json:"membership_ref"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"membership_ref": true}) {
		return
	}
	result, err := service.VantageProvisionMemberCredential(c.Param("tenant"), c.Param("subject"), body.MembershipRef, true)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	quota, err := service.VantageServiceQuota(c.Param("tenant"), result.Credential.Ref)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "created": result.Created, "account": result.Account, "credential": result.Credential, "token_key": result.TokenKey, "quota_setup": gin.H{"contract_revision": "model-service-2", "status": "ready", "quota": quota}})
}
