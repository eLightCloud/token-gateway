package service

import (
	"context"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

type organizationInvoiceTaskIntentKey struct{}
type organizationInvoiceTaskIntent struct {
	id        int64
	quota     int64
	confirmed bool
}

// SettleOrganizationInvoiceTaskSubmission uses the existing settlement/log
// outbox, including the legacy no-session fallback. It records no additional
// consume row and performs no pricing. Confirmation travels in that same
// durable intent so a crash or a failed refund cannot erase its meaning.
func SettleOrganizationInvoiceTaskSubmission(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task, quota int, confirmed bool) error {
	params := BuildTaskConsumptionLog(c, info)
	params.Other.MergePublic(taskBillingOther(task).Snapshot())
	appendTaskLogInfo(task, params.Other)
	params.Other.SetPublic("is_task", true)
	params.Other.SetPublic("task_id", task.TaskID)
	params.Other.SetPublic("request_path", c.Request.URL.Path)
	stage := "reservation"
	if confirmed {
		stage = "final"
	}
	params.Other.SetPublic("billing_stage", stage)
	if taskDeliveredInline(c, task) {
		params.Other.SetPublic("task_sync", true)
	}
	requestContext := context.WithValue(c.Request.Context(), organizationInvoiceTaskIntentKey{}, organizationInvoiceTaskIntent{id: task.ID, quota: int64(quota), confirmed: confirmed})
	c.Request = c.Request.WithContext(requestContext)
	return SettleBillingAndRecordConsumeLog(c, info, quota, params, true)
}

func attachOrganizationInvoiceTaskIntent(ctx context.Context, journal *model.TaskSettlementJournal) {
	if journal.Operation != model.TaskSettlementOperationSettle {
		return
	}
	if intent, ok := ctx.Value(organizationInvoiceTaskIntentKey{}).(organizationInvoiceTaskIntent); ok {
		journal.InvoiceTaskEntity = model.TaskSettlementEntityTask
		journal.InvoiceTaskId = intent.id
		if intent.confirmed {
			journal.InvoiceExpenseQuota = &intent.quota
		}
	}
}
