package common

import (
	"context"
	"sync/atomic"
	"time"
)

// OperationsRequestResult is populated by the final relay outcome, not attempts.
type OperationsRequestResult struct {
	AttemptFailures int64
	Code            string
	Failed          bool
	ServiceFailure  bool
	Ignored         bool
}
type operationsContextKey struct{}
type OperationsRequestOutcome struct {
	PerResult bool
	Result    OperationsRequestResult
	Recorded  bool
}

func WithOperationsOutcome(ctx context.Context, outcome *OperationsRequestOutcome) context.Context {
	return context.WithValue(ctx, operationsContextKey{}, outcome)
}
func SetOperationsOutcome(ctx context.Context, result OperationsRequestResult) {
	if ctx == nil {
		return
	}
	if outcome, ok := ctx.Value(operationsContextKey{}).(*OperationsRequestOutcome); ok && !outcome.PerResult {
		outcome.Result, outcome.Recorded = result, true
	}
}

// OperationsMailConfig returns a copy; a database outage does not require a reload.
func OperationsMailConfig() (EmailDeliveryConfig, error) {
	snapshot := DefaultEmailDelivery.state.Load()
	if snapshot == nil || snapshot.err != nil {
		return EmailDeliveryConfig{}, ErrEmailDeliveryUnavailable
	}
	return snapshot.config, nil
}

type OperationsObservation struct {
	RequestID string
	Group     string
	UserID    int
	Model     string
	Result    OperationsRequestResult
	At        time.Time
}
type OperationsObserver func(OperationsObservation)

var operationsObserver atomic.Pointer[OperationsObserver]

func SetOperationsObserver(observer OperationsObserver) { operationsObserver.Store(&observer) }
func ObserveOperationsRequest(sample OperationsObservation) {
	if observer := operationsObserver.Load(); observer != nil && *observer != nil {
		(*observer)(sample)
	}
}

func OperationsObserverEnabled() bool {
	observer := operationsObserver.Load()
	return observer != nil && *observer != nil
}

type OperationsFaultObserver func(string)

var operationsFaultObserver atomic.Pointer[OperationsFaultObserver]

func SetOperationsFaultObserver(observer OperationsFaultObserver) {
	operationsFaultObserver.Store(&observer)
}
func ReportOperationsFault(kind string) {
	if observer := operationsFaultObserver.Load(); observer != nil && *observer != nil {
		(*observer)(kind)
	}
}

func SetOperationsAttempts(ctx context.Context, failed int) {
	if outcome, ok := ctx.Value(operationsContextKey{}).(*OperationsRequestOutcome); ok && !outcome.PerResult {
		outcome.Result.AttemptFailures = int64(failed)
	}
}

func HasOperationsOutcome(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(operationsContextKey{}).(*OperationsRequestOutcome)
	return ok
}

func OperationsPerResult(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	outcome, ok := ctx.Value(operationsContextKey{}).(*OperationsRequestOutcome)
	return ok && outcome.PerResult
}

// SetOperationsRejection covers early routing failures, preserving an already
// classified final outcome and its attempt history.
func SetOperationsRejection(ctx context.Context, result OperationsRequestResult) {
	if ctx == nil {
		return
	}
	if outcome, ok := ctx.Value(operationsContextKey{}).(*OperationsRequestOutcome); ok && !outcome.PerResult && !outcome.Recorded {
		outcome.Result, outcome.Recorded = result, true
	}
}
