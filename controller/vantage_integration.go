package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// Vantage 专用接入 API 的 HTTP 层：只做协议接线与严格请求体校验，
// 业务语义与幂等裁决在 service/model 层。所有响应都带稳定错误码与
// request_id；秘密只在受控的确保与轮换响应中向 Vantage 后端交付。

func vantageRequestId(c *gin.Context) string {
	return c.GetString(common.RequestIdKey)
}

func vantageRespondError(c *gin.Context, err error) {
	var vantageErr *service.VantageError
	if errors.As(err, &vantageErr) {
		if vantageErr.Status >= http.StatusInternalServerError {
			common.SysError("vantage integration error: " + vantageErr.Error())
		}
		c.JSON(vantageErr.Status, gin.H{
			"success":    false,
			"code":       vantageErr.Code,
			"message":    vantageErr.Message,
			"request_id": vantageRequestId(c),
		})
		return
	}
	common.SysError("vantage integration unexpected error: " + err.Error())
	c.JSON(http.StatusInternalServerError, gin.H{
		"success":    false,
		"code":       service.VantageCodeInternal,
		"message":    "internal error",
		"request_id": vantageRequestId(c),
	})
}

func vantageRespondProvision(c *gin.Context, result *service.VantageProvisionResult) {
	quotaSetup, err := service.VantageProvisionTokenQuota(result)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{
		"success":     true,
		"created":     result.Created,
		"request_id":  vantageRequestId(c),
		"account":     result.Account,
		"credential":  result.Credential,
		"token_key":   result.TokenKey,
		"quota_setup": quotaSetup,
	})
}

func vantageRejectRequest(c *gin.Context, message string) {
	vantageRespondError(c, service.VantageInvalidRequest(message))
}

// vantageBindJSON 只接受合同字段：未知字段（例如 user_id、角色、余额、
// 赠送金额、任意上游 URL）一律 400，不允许请求越权。
func vantageBindJSON(c *gin.Context, dst any, allowedKeys map[string]bool) bool {
	raw, err := c.GetRawData()
	if err != nil {
		vantageRejectRequest(c, "invalid request body")
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return true
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(raw, &fields); err != nil {
		vantageRejectRequest(c, "request body must be a JSON object")
		return false
	}
	for key := range fields {
		if !allowedKeys[key] {
			vantageRejectRequest(c, "unknown field: "+key)
			return false
		}
	}
	if err := common.Unmarshal(raw, dst); err != nil {
		vantageRejectRequest(c, "invalid request body")
		return false
	}
	return true
}

// VantagePutPersonalAccount 确保个人账户及默认 Key。首次 201，恢复 200。
func VantagePutPersonalAccount(c *gin.Context) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"display_name": true}) {
		return
	}
	result, err := service.VantageProvisionPersonalAccount(c.Param("subject"), body.DisplayName)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondProvision(c, result)
}

// VantagePutEnterpriseAccount 确保企业账户，初始余额零；已有账户不覆盖余额。
func VantagePutEnterpriseAccount(c *gin.Context) {
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"display_name": true}) {
		return
	}
	result, err := service.VantageProvisionEnterpriseAccount(c.Param("tenant"), body.DisplayName)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondProvision(c, result)
}

// VantagePutEnterpriseMemberCredential 为当前成员关系确保一个员工 Key。
func VantagePutEnterpriseMemberCredential(c *gin.Context) {
	var body struct {
		MembershipRef string `json:"membership_ref"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"membership_ref": true}) {
		return
	}
	result, err := service.VantageProvisionMemberCredential(c.Param("tenant"), c.Param("subject"), body.MembershipRef)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondProvision(c, result)
}

// VantageGetAccount 查询类型、状态和关联引用，不返回秘密。
func VantageGetAccount(c *gin.Context) {
	account, credentials, err := service.VantageGetAccount(c.Param("accountType") + "/" + c.Param("externalRef"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":     true,
		"request_id":  vantageRequestId(c),
		"account":     account,
		"credentials": credentials,
	})
}

// VantageGetCredential 查询状态、代次和归属，不返回秘密、不回显 Key。
func VantageGetCredential(c *gin.Context) {
	credential, err := service.VantageGetCredential(c.Param("credentialRef"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"request_id": vantageRequestId(c),
		"credential": credential,
	})
}

// VantageRotateCredential 幂等轮换；Idempotency-Key 标识恢复同一次操作。
func VantageRotateCredential(c *gin.Context) {
	opId := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if opId == "" {
		vantageRejectRequest(c, "Idempotency-Key header is required")
		return
	}
	var body struct {
		ExpectedGeneration *int `json:"expected_generation"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"expected_generation": true}) {
		return
	}
	expectedGeneration := 0
	if body.ExpectedGeneration != nil {
		expectedGeneration = *body.ExpectedGeneration
	}
	result, err := service.VantageRotateCredential(c.Param("credentialRef"), opId, expectedGeneration)
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	vantageRespondProvision(c, result)
}

// VantageDeleteCredential 幂等撤销一个凭据，重复请求返回当前状态。
func VantageDeleteCredential(c *gin.Context) {
	credential, err := service.VantageRevokeCredential(c.Param("credentialRef"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"request_id": vantageRequestId(c),
		"credential": credential,
	})
}

// VantageDeleteEnterpriseMemberCredential 撤销整段成员关系；
// 即使 Key 尚未创建也保存撤销事实。
func VantageDeleteEnterpriseMemberCredential(c *gin.Context) {
	credential, err := service.VantageRevokeMembership(c.Param("tenant"), c.Param("subject"), c.Param("membershipRef"))
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"request_id": vantageRequestId(c),
		"credential": credential,
	})
}

// VantageListModels 返回供 Vantage 同步的候选目录安全投影。
// 分页结果必须配合 total/revision 判断完整性，截断页不能当完整目录。
func VantageListModels(c *gin.Context) {
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	pageSize, err := strconv.Atoi(c.Query("page_size"))
	if err != nil || pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	items, revision, err := model.GetVantageModelCatalog()
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	total := len(items)
	// 避免 page 的乘法溢出；越界页保持空列表而非回落到第一页。
	offset := total
	if page-1 <= total/pageSize {
		offset = min((page-1)*pageSize, total)
	}
	items = items[offset:min(offset+pageSize, total)]
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"request_id": vantageRequestId(c),
		"total":      total,
		"revision":   revision,
		"page":       page,
		"page_size":  pageSize,
		"items":      items,
	})
}
