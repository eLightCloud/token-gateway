package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// Routing already records failed attempts. Observability only counts those
// events; it never makes a retry decision or writes a second request outcome.
func RecordOperationsRoutingAttempts(c *gin.Context) {
	if c == nil || c.Request == nil || !common.HasOperationsOutcome(c.Request.Context()) {
		return
	}
	failed := 0
	for _, event := range RequestPolicy(c).Events() {
		if event.Decision.Action == "failure" {
			failed++
		}
	}
	common.SetOperationsAttempts(c.Request.Context(), failed)
}

func RecordOperationsRoutingRejection(c *gin.Context, apiErr *types.NewAPIError) {
	if c == nil || c.Request == nil || apiErr == nil || apiErr.GetErrorCode() != types.ErrorCodeGetChannelFailed {
		return
	}
	common.SetOperationsRejection(c.Request.Context(), common.OperationsRequestResult{Code: string(apiErr.GetErrorCode()), Failed: true, ServiceFailure: true})
}
