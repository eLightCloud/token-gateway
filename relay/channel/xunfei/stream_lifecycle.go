package xunfei

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func xunfeiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, textRequest dto.GeneralOpenAIRequest, appId string, apiSecret string, apiKey string) (*dto.Usage, *types.NewAPIError) {
	domain, authURL := getXunfeiAuthUrl(c, apiKey, apiSecret, textRequest.Model)
	stream, err := xunfeiMakeRequest(textRequest, domain, authURL, appId)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	return consumeXunfeiStream(c, info, stream)
}

func consumeXunfeiStream(c *gin.Context, info *relaycommon.RelayInfo, stream *xunfeiStreamConnection) (*dto.Usage, *types.NewAPIError) {
	defer stream.close()
	helper.SetEventStreamHeaders(c)
	info.StreamStatus = relaycommon.NewStreamStatus()
	var usage dto.Usage
	hasUpstreamUsage := false
	clientDone := c.Request.Context().Done()
	var idleTimer *time.Timer
	var absoluteTimer *time.Timer
	var idleC <-chan time.Time
	var absoluteC <-chan time.Time
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
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
		drainTimeout = helper.TextStreamDrainTimeout(c)
		idleTimer = time.NewTimer(drainTimeout)
		absoluteTimer = time.NewTimer(drainTimeout)
		idleC = idleTimer.C
		absoluteC = absoluteTimer.C
	}
	for {
		if clientDone != nil && c.Request.Context().Err() != nil {
			beginDrain()
		}
		select {
		case <-clientDone:
			beginDrain()
		case xunfeiResponse := <-stream.data:
			if clientDone != nil && c.Request.Context().Err() != nil {
				beginDrain()
			}
			recognized := xunfeiResponse.Header.Sid != "" || xunfeiResponse.Header.Status != 0 || xunfeiResponse.Payload.Choices.Status != 0 || xunfeiResponse.Payload.Choices.Seq != 0 || len(xunfeiResponse.Payload.Choices.Text) != 0 || xunfeiResponse.Payload.Usage.Text != nil
			if idleTimer != nil && xunfeiResponse.Header.Code == 0 && recognized {
				businessAfterDrain = true
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(drainTimeout)
			}
			if xunfeiResponse.Header.Code != 0 {
				protocolErr := fmt.Errorf("xunfei stream error %d: %s", xunfeiResponse.Header.Code, xunfeiResponse.Header.Message)
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultProtocolFailure, protocolErr)
				return nil, types.NewError(protocolErr, types.ErrorCodeBadResponse)
			}
			if upstreamUsage := xunfeiResponse.Payload.Usage.Text; upstreamUsage != nil {
				usage.PromptTokens += upstreamUsage.PromptTokens
				usage.CompletionTokens += upstreamUsage.CompletionTokens
				usage.TotalTokens += upstreamUsage.TotalTokens
				hasUpstreamUsage = hasUpstreamUsage || dto.HasOpenAIUsageTokens(upstreamUsage)
			}
			response := streamResponseXunfei2OpenAI(&xunfeiResponse)
			if err := helper.ObjectData(c, response); err != nil {
				common.SysLog("error writing stream response: " + err.Error())
			}
			if xunfeiResponse.Payload.Choices.Status == 2 {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				if hasUpstreamUsage {
					info.StreamStatus.MarkUsageComplete()
				}
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultTerminalSuccess, nil)
				helper.Done(c)
				return &usage, nil
			}
		case streamErr := <-stream.errs:
			if clientDone != nil && c.Request.Context().Err() != nil {
				beginDrain()
			}
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, streamErr)
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultScannerError, streamErr)
			return nil, types.NewError(streamErr, types.ErrorCodeBadResponse)
		case <-idleC:
			timeoutErr := fmt.Errorf("xunfei stream idle after client disconnect")
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultIdleTimeout, timeoutErr)
			return nil, types.NewError(timeoutErr, types.ErrorCodeBadResponse)
		case <-absoluteC:
			if !businessAfterDrain {
				timeoutErr := fmt.Errorf("xunfei stream idle after client disconnect")
				info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultIdleTimeout, timeoutErr)
				return nil, types.NewError(timeoutErr, types.ErrorCodeBadResponse)
			}
			timeoutErr := fmt.Errorf("xunfei stream drain deadline exceeded")
			info.StreamStatus.SetUpstreamResult(relaycommon.StreamUpstreamResultDrainTimeout, timeoutErr)
			return nil, types.NewError(timeoutErr, types.ErrorCodeBadResponse)
		}
	}
}

type xunfeiStreamConnection struct {
	data  <-chan XunfeiChatResponse
	errs  <-chan error
	close func()
}

func xunfeiMakeRequest(textRequest dto.GeneralOpenAIRequest, domain, authURL, appId string) (*xunfeiStreamConnection, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(authURL, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("xunfei websocket handshake failed")
	}

	data := requestOpenAI2Xunfei(textRequest, appId, domain)
	if err = conn.WriteJSON(data); err != nil {
		_ = conn.Close()
		return nil, err
	}

	dataChan := make(chan XunfeiChatResponse, 1)
	errChan := make(chan error, 1)
	cancelRead := make(chan struct{})
	readDone := make(chan struct{})
	var closeOnce sync.Once
	closeStream := func() {
		closeOnce.Do(func() {
			close(cancelRead)
			_ = conn.Close()
		})
		<-readDone
	}
	go func() {
		defer close(readDone)
		defer conn.Close()
		for {
			_, msg, readErr := conn.ReadMessage()
			if readErr != nil {
				select {
				case errChan <- readErr:
				case <-cancelRead:
				}
				return
			}
			var response XunfeiChatResponse
			if readErr = common.Unmarshal(msg, &response); readErr != nil {
				select {
				case errChan <- readErr:
				case <-cancelRead:
				}
				return
			}
			select {
			case dataChan <- response:
			case <-cancelRead:
				return
			}
			if response.Payload.Choices.Status == 2 {
				return
			}
		}
	}()

	return &xunfeiStreamConnection{data: dataChan, errs: errChan, close: closeStream}, nil
}
