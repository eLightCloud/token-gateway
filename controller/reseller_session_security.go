package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// An empty target binds the proof to signing out other sessions.
func requireResellerSessionRevocationProof(c *gin.Context, sid string) bool {
	if !middleware.IsResellerSelfAccount(c) {
		return true
	}
	context, err := common.Marshal(service.SessionRevocationContext{SessionID: sid})
	if err != nil {
		writeSecurityOperationError(c, err)
		return false
	}
	return middleware.RequireSecurityProof(c, service.VerificationOperation{
		Scope: service.VerificationScopeSessionRevoke, Context: context,
	}) != nil
}
