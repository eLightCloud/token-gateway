package service

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const PricingChannelUnavailableCode relaytypes.ErrorCode = "pricing_channel_unavailable"

func PricingChannelUnavailable(c *gin.Context, name string) *relaytypes.NewAPIError {
	message := "No eligible channel is available for this model"
	if c != nil {
		group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
		message = i18n.T(c, i18n.MsgDistributorNoAvailableChannel, map[string]any{"Group": group, "Model": name})
		logger.LogWarn(c, common.LogText("pricing channel unavailable: model=%s group=%s", name, group))
	}
	return relaytypes.NewErrorWithStatusCode(errors.New(message), PricingChannelUnavailableCode, http.StatusServiceUnavailable, relaytypes.ErrOptionWithSkipRetry())
}

func pricingScopeSelectionError(c *gin.Context, name string, channel *model.Channel) *ChannelSelectError {
	value, _ := c.Get(personalPricingContextKey)
	snapshot, _ := value.(*hosttypes.PersonalPricingSnapshot)
	if !snapshot.RestrictsChannels() {
		return nil
	}
	err := PricingChannelUnavailable(c, name)
	return &ChannelSelectError{StatusCode: err.StatusCode, Code: PricingChannelUnavailableCode, Message: err.Err.Error(),
		FilterKind: dto.FilterPersonalAuthorization, Channel: channel}
}

func checkPersonalPricingChannel(c *gin.Context, info *relaycommon.RelayInfo) *relaytypes.NewAPIError {
	if info.ChannelMeta != nil && info.PersonalPricing.RestrictsChannels() {
		if _, ok := info.PersonalPricing.Channels[info.ChannelId]; !ok {
			return PricingChannelUnavailable(c, info.OriginModelName)
		}
	}
	return nil
}

// CheckLockedTaskPricing verifies an existing task's channel against the new
// submission's pricing scope before calling the submit adaptor.
func CheckLockedTaskPricing(c *gin.Context, info *relaycommon.RelayInfo, channelID int) *relaytypes.NewAPIError {
	if info.UserId <= 0 || info.OriginModelName == "" {
		return nil
	}
	snapshot, err := LoadPersonalRequestPricing(c, info.UserId, info.OriginModelName)
	if err != nil {
		status, code, message := personalPricingRejection(err)
		return relaytypes.NewErrorWithStatusCode(errors.New(message), code, status, relaytypes.ErrOptionWithSkipRetry())
	}
	if snapshot.RestrictsChannels() {
		if _, ok := snapshot.Channels[channelID]; !ok {
			return PricingChannelUnavailable(c, info.OriginModelName)
		}
	}
	return nil
}
