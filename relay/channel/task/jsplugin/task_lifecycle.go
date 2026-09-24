package jsplugin

import (
	"context"
	"github.com/QuantumNous/new-api/model"
	"net/http"
)

// FetchTaskDelete issues the driver's cancel/delete request for one task. The
// hook decides whether the pinned southbound contract supports the operation
// at all: unsupported profiles fail the hook and surface as a capability
// error instead of a silent local-only transition.
func (a *TaskAdaptor) FetchTaskDelete(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	ctx, err := a.queryContext(task, key, baseURL, proxy)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "buildDeleteRequest", ctx)
	if err != nil {
		return nil, err
	}
	return a.doFetchDescriptor(baseURL, proxy, value)
}
