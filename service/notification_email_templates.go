package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

func notificationEmailKind(event string) common.EmailKind {
	switch event {
	case dto.NotifyTypeQuotaExceed:
		return common.EmailQuotaWarning
	case dto.NotifyTypeChannelTest:
		return common.EmailChannelTest
	case dto.NotifyTypeChannelUpdate:
		return common.EmailModelUpdate
	}
	if strings.HasPrefix(event, dto.NotifyTypeChannelUpdate+"_") {
		return common.EmailChannelStatus
	}
	return common.EmailNotice
}
