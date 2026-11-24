package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"sync"
)

type personalRealtimeReservation struct {
	mu   sync.Mutex
	base int
}

const personalRealtimeReservationKey = "reseller.realtime_reservation"

// The legacy Realtime increment writes wallets directly. Personal requests
// instead reserve the cumulative original-engine amount in their billing
// session; the ordinary final engine remains the sole settlement authority.
func reservePersonalRealtimeUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.RealtimeUsage) (int, error) {
	if info.UsePrice {
		return 0, nil
	}
	value, ok := c.Get(personalRealtimeReservationKey)
	if !ok || info.Billing == nil {
		return 0, fmt.Errorf("personal realtime reservation unavailable")
	}
	state := value.(*personalRealtimeReservation)
	state.mu.Lock()
	defer state.mu.Unlock()
	quota, clamp := calculateAudioQuota(QuotaInfo{InputDetails: TokenDetails{TextTokens: usage.InputTokenDetails.TextTokens, AudioTokens: usage.InputTokenDetails.AudioTokens}, OutputDetails: TokenDetails{TextTokens: usage.OutputTokenDetails.TextTokens, AudioTokens: usage.OutputTokenDetails.AudioTokens}, ModelName: info.OriginModelName, UsePrice: info.UsePrice, ModelRatio: info.PriceData.ModelRatio, GroupRatio: info.PriceData.GroupRatioInfo.GroupRatio})
	noteQuotaClamp(info, clamp)
	if clamp != nil {
		return 0, clamp
	}
	if quota > common.MaxQuota-state.base {
		return 0, fmt.Errorf("personal realtime quota exceeds limit")
	}
	target := state.base + quota
	if err := info.Billing.Reserve(target); err != nil {
		return 0, err
	}
	state.base = target
	return quota, nil
}
