package aws

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimeTypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

func awsStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, a *Adaptor) (*types.NewAPIError, *dto.Usage) {
	requestContext := c.Request.Context()
	invokeContext := newAwsStreamInvokeContext(requestContext)
	defer invokeContext.Close()

	awsResp, err := a.AwsClient.InvokeModelWithResponseStream(invokeContext, a.AwsReq.(*bedrockruntime.InvokeModelWithResponseStreamInput))
	if err != nil {
		return newAwsInvokeError(requestContext, err, "InvokeModelWithResponseStream"), nil
	}
	invokeContext.MarkEstablished()
	stream := awsResp.GetStream()
	defer stream.Close()
	info.StreamStatus = relaycommon.NewStreamStatus()

	claudeInfo := &claude.ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}

	events := stream.Events()
	clientDone := requestContext.Done()
	var idleTimer *time.Timer
	var absoluteTimer *time.Timer
	var idleC <-chan time.Time
	var absoluteC <-chan time.Time
	terminal := false
	terminalUsageSeen := false
	businessAfterDrain := false
	drainTimeout := time.Duration(0)
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
		if absoluteTimer != nil {
			absoluteTimer.Stop()
		}
	}()
	beginDrain := func() {
		if clientDone == nil {
			return
		}
		clientDone = nil
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, requestContext.Err())
		drainTimeout = helper.TextStreamDrainTimeout(c)
		idleTimer = time.NewTimer(drainTimeout)
		absoluteTimer = time.NewTimer(drainTimeout)
		idleC = idleTimer.C
		absoluteC = absoluteTimer.C
	}
	for {
		if clientDone != nil && requestContext.Err() != nil {
			beginDrain()
		}
		select {
		case <-clientDone:
			beginDrain()
		case <-invokeContext.Done():
			if clientDone != nil && requestContext.Err() != nil {
				beginDrain()
			}
			invokeErr := invokeContext.Err()
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultScannerError, invokeErr)
			return types.NewError(invokeErr, types.ErrorCodeAwsInvokeError), nil
		case event, ok := <-events:
			if clientDone != nil && requestContext.Err() != nil {
				beginDrain()
			}
			if !ok {
				if streamErr := stream.Err(); streamErr != nil {
					info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultScannerError, streamErr)
					return types.NewError(streamErr, types.ErrorCodeBadResponse), nil
				}
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultIncompleteEOF, nil)
				claude.HandleStreamFinalResponse(c, info, claudeInfo)
				return nil, claudeInfo.Usage
			}
			switch v := event.(type) {
			case *bedrockruntimeTypes.ResponseStreamMemberChunk:
				var envelope struct {
					Type string `json:"type"`
				}
				recognized := common.Unmarshal(v.Value.Bytes, &envelope) == nil && envelope.Type != ""
				if idleTimer != nil && recognized {
					businessAfterDrain = true
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(drainTimeout)
				}
				if claude.HasAuthoritativeStreamUsage(v.Value.Bytes) {
					terminalUsageSeen = true
				}
				info.SetFirstResponseTime()
				respErr := claude.HandleStreamResponseData(c, info, claudeInfo, string(v.Value.Bytes))
				if respErr != nil {
					info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultHandlerError, respErr)
					return respErr, nil
				}
				if envelope.Type == "message_stop" {
					terminal = true
					if terminalUsageSeen {
						info.StreamStatus.MarkUsageComplete()
					}
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
					info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultTerminalSuccess, nil)
				}
			case *bedrockruntimeTypes.UnknownUnionMember:
				logger.LogError(c, "unknown AWS event-stream tag: "+v.Tag)
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultHandlerError, errors.New("unknown response type"))
				return types.NewError(errors.New("unknown response type"), types.ErrorCodeInvalidRequest), nil
			default:
				logger.LogError(c, "AWS event stream returned nil or unknown event type")
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultHandlerError, errors.New("nil or unknown response type"))
				return types.NewError(errors.New("nil or unknown response type"), types.ErrorCodeInvalidRequest), nil
			}
			if terminal {
				claude.HandleStreamFinalResponse(c, info, claudeInfo)
				return nil, claudeInfo.Usage
			}
		case <-idleC:
			timeoutErr := fmt.Errorf("AWS event stream idle after client disconnect")
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultIdleTimeout, timeoutErr)
			return types.NewError(timeoutErr, types.ErrorCodeBadResponse), nil
		case <-absoluteC:
			if !businessAfterDrain {
				timeoutErr := fmt.Errorf("AWS event stream idle after client disconnect")
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultIdleTimeout, timeoutErr)
				return types.NewError(timeoutErr, types.ErrorCodeBadResponse), nil
			}
			timeoutErr := fmt.Errorf("AWS event stream drain deadline exceeded")
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultDrainTimeout, timeoutErr)
			return types.NewError(timeoutErr, types.ErrorCodeBadResponse), nil
		}
	}
}
