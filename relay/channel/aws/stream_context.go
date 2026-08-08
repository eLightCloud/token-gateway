package aws

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

type awsStreamInvokeContext struct {
	context.Context
	cancel          context.CancelFunc
	established     chan struct{}
	establishedOnce sync.Once
}

func newAwsStreamInvokeContext(parent context.Context) *awsStreamInvokeContext {
	base := context.Background()
	var ctx context.Context
	var cancel context.CancelFunc
	if common.RelayTimeout > 0 {
		ctx, cancel = context.WithTimeout(base, time.Duration(common.RelayTimeout)*time.Second)
	} else {
		ctx, cancel = context.WithCancel(base)
	}
	streamContext := &awsStreamInvokeContext{
		Context:     ctx,
		cancel:      cancel,
		established: make(chan struct{}),
	}
	go func() {
		select {
		case <-parent.Done():
			streamContext.cancel()
		case <-streamContext.established:
		case <-ctx.Done():
		}
	}()
	return streamContext
}

func (c *awsStreamInvokeContext) MarkEstablished() {
	if c == nil {
		return
	}
	c.establishedOnce.Do(func() { close(c.established) })
}

func (c *awsStreamInvokeContext) Close() {
	if c == nil {
		return
	}
	c.MarkEstablished()
	c.cancel()
}
