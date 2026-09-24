package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// Delivery is independent of generation and settlement. Convey a persisted
// access-contract gap without inventing a ModelArk error or proxy URL.
func applyTaskDeliveryHeaders(c *gin.Context, tasks []*model.Task) {
	for _, task := range tasks {
		if task == nil || !task.ResultRetrievable() {
			continue
		}
		if task.UsagePending {
			c.Header("X-NewAPI-Usage-Reconciliation", "pending")
		}
		var state struct {
			Delivery struct {
				Status string `json:"status"`
			} `json:"delivery"`
		}
		if common.Unmarshal(task.PrivateData.PluginState, &state) == nil && state.Delivery.Status == "unavailable" {
			c.Header("X-NewAPI-Media-Delivery", "unavailable")
		}
	}
}
