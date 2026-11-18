package service

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
)

// Keep the pre-clamp procurement amount in server diagnostics. Reservation
// callers may pass the returned error directly to customers.
func rejectResellerProcurementReservation(requestID string, clamp *common.QuotaClamp) *relaytypes.NewAPIError {
	common.SysError(fmt.Sprintf("reseller procurement reservation rejected: request_id=%s %s", requestID, clamp))
	err := model.ErrResellerProcurementInsufficient
	status, code, _ := personalPricingRejection(err)
	return relaytypes.NewErrorWithStatusCode(err, code, status, relaytypes.ErrOptionWithSkipRetry())
}
