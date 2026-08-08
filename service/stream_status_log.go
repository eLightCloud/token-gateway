package service

import relaycommon "github.com/QuantumNous/new-api/relay/common"

// appendStreamUpstreamResult preserves the upstream stream outcome separately
// from the first downstream end reason. A client_gone request can therefore
// still be audited as terminal_success, usage_missing, or another upstream
// failure without overwriting the client-side fact.
func appendStreamUpstreamResult(status *relaycommon.StreamStatus, streamInfo map[string]interface{}) {
	if status == nil || streamInfo == nil {
		return
	}

	result, upstreamErr := status.GetUpstreamResult()
	if result != relaycommon.StreamUpstreamResultNone {
		streamInfo["upstream_result"] = string(result)
	}
	if upstreamErr != nil {
		streamInfo["upstream_error"] = upstreamErr.Error()
	}
}
