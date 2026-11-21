package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	chatReceiptWriterKey = "chat_receipt_writer"
	maxRelayReplayBytes  = 4 << 20
	openAIStreamDone     = "data: [DONE]\n\n"
)

// GuardChatRelay runs after token authentication. It owns the HTTP success
// boundary for OpenAI Chat and Completions, including their SSE terminal frame.
func GuardChatRelay(c *gin.Context) {
	proof := &service.RelaySettlement{}
	if keys := c.Request.Header.Values("Idempotency-Key"); len(keys) != 0 {
		if replayOrClaimChat(c, proof, keys) {
			c.Abort()
			return
		}
		// The key is scoped to this gateway's credential. An upstream shared
		// by different customers must not receive their unscoped client keys.
		c.Request.Header.Del("Idempotency-Key")
	}

	writer := &chatReceiptWriter{
		ResponseWriter: c.Writer,
		header:         c.Writer.Header().Clone(),
		status:         c.Writer.Status(),
		size:           -1,
		recordStream:   proof.Operation != nil,
	}
	proof.Result = func() *model.RelayOperationResult {
		// A reply plugin can stop the stream after the upstream terminal frame
		// has been held. Keep the plugin's error framing out of success replay.
		if writer.stream && c.Request.Context().Err() != nil {
			return nil
		}
		return writer.result()
	}
	c.Request = c.Request.WithContext(service.WithRelaySettlement(c.Request.Context(), proof))
	c.Set(chatReceiptWriterKey, writer)
	helper.HoldOpenAIStreamDone(c, func() { writer.terminal = true })
	c.Writer = writer
	defer func() { c.Writer = writer.ResponseWriter }()
	c.Next()

	if !proof.Durable && !writer.stream && writer.status >= 200 && writer.status < 300 {
		writer.discard()
		c.Writer = writer
		writeChatReceiptError(c, http.StatusServiceUnavailable, "billing_settlement_unavailable", "Billing settlement is unavailable.")
	}
	if proof.Operation != nil && !proof.Durable && !proof.Uncertain && (writer.stream || writer.status >= 400) {
		var result *model.RelayOperationResult
		if !writer.stream {
			result = writer.result()
		}
		if err := model.FailRelayOperation(*proof.Operation, result); err != nil {
			logger.LogWarn(c, "could not save relay operation failure: "+err.Error())
		}
	}
	if writer.stream {
		if proof.Durable && writer.terminal {
			helper.HoldOpenAIStreamDone(c, nil)
			helper.Done(c)
		}
		return
	}
	writer.release()
}

func replayOrClaimChat(c *gin.Context, proof *service.RelaySettlement, keys []string) bool {
	key := keys[0]
	if key == "" || len(key) > 255 || len(keys) != 1 ||
		strings.ContainsAny(key, "\r\n\t") || strings.TrimSpace(key) != key {
		writeChatReceiptError(c, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be a single value of 1 to 255 bytes.")
		return true
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		status := http.StatusBadRequest
		if common.IsRequestBodyTooLargeError(err) || errors.Is(err, common.ErrRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeChatReceiptError(c, status, "invalid_request_body", "Could not read request body.")
		return true
	}
	if !middleware.AuthorizeKeyedChatModel(c) {
		return true
	}
	id := c.GetString(common.RequestIdKey)
	if id == "" {
		id = common.NewRequestId()
		c.Set(common.RequestIdKey, id)
		c.Header(common.RequestIdKey, id)
	}
	keyHash := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%s\x00%s", c.GetInt("id"), c.GetInt("token_id"), c.Request.URL.Path, key)))
	digest := sha256.New()
	if pin, pinned, _ := service.GetChannelConstraints(c).ResolvedPin(); pinned {
		_, _ = fmt.Fprintf(digest, "%d\x00", pin.ChannelId)
	} else {
		_, _ = io.WriteString(digest, "\x00")
	}
	for _, value := range []string{c.Request.URL.RawQuery, c.ContentType(), c.GetHeader("OpenAI-Organization"), c.GetHeader("OpenAI-Project")} {
		_, _ = io.WriteString(digest, value+"\x00")
	}
	body, err := storage.NewReader()
	if err == nil {
		_, err = io.Copy(digest, body)
		_ = body.Close()
	}
	if err != nil {
		writeChatReceiptError(c, http.StatusBadRequest, "invalid_request_body", "Could not read request body.")
		return true
	}
	requestHash := hex.EncodeToString(digest.Sum(nil))
	operation, err := model.ClaimRelayOperation(model.RelayOperation{
		KeyHash: hex.EncodeToString(keyHash[:]), RequestHash: requestHash, RequestID: id,
	})
	switch {
	case err != nil:
		logger.LogWarn(c, "could not claim relay operation: "+err.Error())
		writeChatReceiptError(c, http.StatusServiceUnavailable, "idempotency_unavailable", "Could not claim the request. Retry with the same Idempotency-Key.")
	case operation.RequestHash != requestHash:
		writeChatReceiptError(c, http.StatusConflict, "idempotency_key_conflict", "This Idempotency-Key belongs to a different request.")
	case operation.RequestID == id:
		proof.Operation = &operation
		return false
	case operation.CompletedAt == nil:
		c.Header(common.RequestIdKey, operation.RequestID)
		c.Header("Retry-After", "1")
		writeChatReceiptError(c, http.StatusConflict, "idempotency_in_progress", "The original request outcome is pending. Retry with the same Idempotency-Key.")
	case len(operation.Result) == 0:
		writeChatReplayUnavailable(c, operation.RequestID)
	default:
		result, err := operation.Replay()
		if err != nil {
			logger.LogWarn(c, "could not replay relay operation: "+err.Error())
			writeChatReceiptError(c, http.StatusServiceUnavailable, "idempotency_unavailable", "Could not recover the original response.")
			return true
		}
		// Earlier versions could save a 200 response for an incomplete stream.
		if strings.HasPrefix(result.ContentType, "text/event-stream") {
			if !bytes.HasSuffix(result.Body, []byte(openAIStreamDone)) {
				writeChatReplayUnavailable(c, operation.RequestID)
				return true
			}
			helper.SetEventStreamHeaders(c)
		}
		c.Header(common.RequestIdKey, operation.RequestID)
		if result.ContentEncoding != "" {
			c.Header("Content-Encoding", result.ContentEncoding)
		}
		c.Data(result.Status, result.ContentType, result.Body)
	}
	return true
}

func writeChatReplayUnavailable(c *gin.Context, requestID string) {
	c.Header(common.RequestIdKey, requestID)
	writeChatReceiptError(c, http.StatusConflict, "idempotency_response_unavailable", "The original request finished, but its response is unavailable for replay.")
}

func writeChatReceiptError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": "new_api_error", "code": code, "message": message}})
}

// settleChatBeforeSuccess runs after the reply plugins have finished writing.
// A log delivery error after a durable settlement leaves the success intact.
func settleChatBeforeSuccess(c *gin.Context) *types.NewAPIError {
	proof := service.RelaySettlementFor(c.Request.Context())
	if proof == nil {
		return nil
	}
	if proof.Commit != nil {
		if err := proof.Commit(); err != nil {
			logger.LogWarn(c, "relay settlement: "+err.Error())
		}
	}
	if proof.Durable {
		return nil
	}
	raw, _ := c.Get(chatReceiptWriterKey)
	writer := raw.(*chatReceiptWriter)
	if !writer.stream {
		writer.discard()
	}
	c.Writer = writer
	return types.NewErrorWithStatusCode(errors.New("Billing settlement is unavailable"), types.ErrorCode("billing_settlement_unavailable"),
		http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

type chatReceiptWriter struct {
	gin.ResponseWriter
	header       http.Header
	body         bytes.Buffer
	status       int
	size         int
	recordStream bool
	stream       bool
	terminal     bool
	overflow     bool
	interrupted  bool
}

func (w *chatReceiptWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *chatReceiptWriter) Header() http.Header { return w.header }
func (w *chatReceiptWriter) Status() int         { return w.status }
func (w *chatReceiptWriter) Size() int           { return w.size }
func (w *chatReceiptWriter) Written() bool       { return w.size >= 0 }

func (w *chatReceiptWriter) discard() {
	w.header = w.ResponseWriter.Header().Clone()
	w.status = w.ResponseWriter.Status()
	w.size = -1
	w.body.Reset()
	w.overflow = false
}

func (w *chatReceiptWriter) WriteHeader(status int) {
	if status > 0 && !w.Written() {
		w.status = status
	}
}

func (w *chatReceiptWriter) WriteHeaderNow() {
	if w.Written() {
		return
	}
	w.size = 0
	if w.status == http.StatusOK && strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		w.stream = true
		w.copyHeaders()
		w.ResponseWriter.WriteHeader(w.status)
		w.ResponseWriter.WriteHeaderNow()
	}
}

func (w *chatReceiptWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	if !w.stream {
		_, _ = w.body.Write(data)
	} else if w.recordStream && !w.overflow {
		if len(data) > maxRelayReplayBytes-w.body.Len() {
			w.overflow = true
			w.body = bytes.Buffer{}
		} else {
			_, _ = w.body.Write(data)
		}
	}
	w.size += len(data)
	if w.stream {
		n, err := w.ResponseWriter.Write(data)
		if n != len(data) || err != nil {
			w.interrupted = true
		}
		return n, err
	}
	return len(data), nil
}

func (w *chatReceiptWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *chatReceiptWriter) Flush() {
	w.WriteHeaderNow()
	if w.stream {
		w.ResponseWriter.Flush()
	}
}

func (w *chatReceiptWriter) copyHeaders() {
	header := w.ResponseWriter.Header()
	clear(header)
	for name, values := range w.header {
		header[name] = values
	}
}

func (w *chatReceiptWriter) release() {
	w.copyHeaders()
	w.ResponseWriter.WriteHeader(w.status)
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
}

func (w *chatReceiptWriter) result() *model.RelayOperationResult {
	body := w.body.Bytes()
	if len(body) > maxRelayReplayBytes || w.overflow {
		return nil
	}
	if w.stream {
		if w.interrupted || !w.terminal || len(body)+len(openAIStreamDone) > maxRelayReplayBytes {
			return nil
		}
		body = append(body, openAIStreamDone...)
	}
	return &model.RelayOperationResult{Status: w.status, ContentType: w.header.Get("Content-Type"),
		ContentEncoding: w.header.Get("Content-Encoding"), Body: body}
}
