package perfmetrics

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// Business rejections remain user errors while client cancellation is ignored.
// This observes the existing final-outcome classification without changing it.
func recordOperationsRelayOutcome(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError, outcome Outcome) {
	if !common.HasOperationsOutcome(ctx) {
		return
	}
	code := ""
	if apiErr != nil {
		code = string(apiErr.GetErrorCode())
	}
	cancelled := errors.Is(ctx.Err(), context.Canceled) || apiErr != nil && errors.Is(apiErr, context.Canceled)
	stream := info.StreamStatus.OutcomeSnapshot()
	deadline := errors.Is(ctx.Err(), context.DeadlineExceeded) || info.StreamStatus != nil && errors.Is(info.StreamStatus.EndError, context.DeadlineExceeded)
	if stream.Response == relaycommon.ResponseOutcomeCancelled || stream.EndReason == relaycommon.StreamEndReasonPingFail || stream.EndReason == relaycommon.StreamEndReasonClientGone && !deadline {
		cancelled = true
	}
	result := common.OperationsRequestResult{Code: code, Failed: !cancelled && (outcome == OutcomeFailure || apiErr != nil), ServiceFailure: !cancelled && outcome == OutcomeFailure, Ignored: cancelled || outcome == OutcomeIgnored && apiErr == nil}
	if common.OperationsPerResult(ctx) {
		common.ObserveOperationsRequest(common.OperationsObservation{UserID: info.UserId, Model: info.OriginModelName, Group: info.UsingGroup, RequestID: info.RequestId, At: time.Now(), Result: result})
		return
	}
	common.SetOperationsOutcome(ctx, result)
}
