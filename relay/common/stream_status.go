package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

type StreamUpstreamResult string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

const (
	StreamUpstreamResultNone            StreamUpstreamResult = ""
	StreamUpstreamResultTerminalSuccess StreamUpstreamResult = "terminal_success"
	StreamUpstreamResultProtocolFailure StreamUpstreamResult = "protocol_failure"
	StreamUpstreamResultUsageMissing    StreamUpstreamResult = "usage_missing"
	StreamUpstreamResultIncompleteEOF   StreamUpstreamResult = "incomplete_eof"
	StreamUpstreamResultScannerError    StreamUpstreamResult = "scanner_error"
	StreamUpstreamResultHandlerError    StreamUpstreamResult = "handler_error"
	StreamUpstreamResultIdleTimeout     StreamUpstreamResult = "idle_timeout"
	StreamUpstreamResultDrainTimeout    StreamUpstreamResult = "drain_timeout"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu         sync.Mutex
	Errors     []StreamErrorEntry
	ErrorCount int

	upstreamMu     sync.RWMutex
	upstreamResult StreamUpstreamResult
	upstreamError  error
	usageComplete  bool
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.EndReason = reason
		s.EndError = err
	})
}

func (s *StreamStatus) SetUpstreamResult(result StreamUpstreamResult, err error) {
	if s == nil || result == StreamUpstreamResultNone {
		return
	}
	s.upstreamMu.Lock()
	defer s.upstreamMu.Unlock()
	if s.upstreamResult != StreamUpstreamResultNone &&
		!(s.upstreamResult == StreamUpstreamResultTerminalSuccess && result == StreamUpstreamResultUsageMissing) {
		return
	}
	s.upstreamResult = result
	s.upstreamError = err
}

func (s *StreamStatus) GetUpstreamResult() (StreamUpstreamResult, error) {
	if s == nil {
		return StreamUpstreamResultNone, nil
	}
	s.upstreamMu.RLock()
	defer s.upstreamMu.RUnlock()
	return s.upstreamResult, s.upstreamError
}

func (s *StreamStatus) MarkUsageComplete() {
	if s == nil {
		return
	}
	s.upstreamMu.Lock()
	s.usageComplete = true
	s.upstreamMu.Unlock()
}

func (s *StreamStatus) IsUsageComplete() bool {
	if s == nil {
		return false
	}
	s.upstreamMu.RLock()
	defer s.upstreamMu.RUnlock()
	return s.usageComplete
}

func (s *StreamStatus) FinalizeUpstreamUsage() StreamUpstreamResult {
	if s == nil {
		return StreamUpstreamResultNone
	}
	s.upstreamMu.Lock()
	defer s.upstreamMu.Unlock()
	if s.upstreamResult == StreamUpstreamResultTerminalSuccess && !s.usageComplete {
		s.upstreamResult = StreamUpstreamResultUsageMissing
		s.upstreamError = fmt.Errorf("terminal stream response did not contain complete upstream usage")
	}
	return s.upstreamResult
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	upstreamResult, upstreamErr := s.GetUpstreamResult()
	if upstreamResult != StreamUpstreamResultNone {
		fmt.Fprintf(b, " upstream_result=%s", upstreamResult)
	}
	if upstreamErr != nil {
		fmt.Fprintf(b, " upstream_error=%q", upstreamErr.Error())
	}
	s.mu.Lock()
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	s.mu.Unlock()
	return b.String()
}
