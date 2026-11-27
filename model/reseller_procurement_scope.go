package model

// An older client can round-trip a stored 1 without sending the explicit flag.
// The flag distinguishes a configured base price from the authorization default.
func procurementBaseRatioConfigured(item ResellerConfigItem, ratio int64, previous bool, procurement bool) (bool, error) {
	if !procurement {
		if item.RatioConfigured != nil {
			return false, ErrResellerInvalid
		}
		return false, nil
	}
	if item.RatioConfigured == nil {
		return item.Ratio != "" && ratio == ResellerRatioScale && previous, nil
	}
	if !*item.RatioConfigured {
		if ratio != ResellerRatioScale {
			return false, ErrResellerInvalid
		}
		return false, nil
	}
	if item.Ratio == "" {
		return false, ErrResellerInvalid
	}
	return ratio == ResellerRatioScale, nil
}

func (row ResellerModelConfig) hasConfiguredProcurementBaseRatio() bool {
	return row.ProcurementBaseRatioConfigured != nil && *row.ProcurementBaseRatioConfigured
}

// Deleted channels retain their history and can have their procurement rule
// cleared or grant revoked. The missing channel cannot acquire a new grant or rate.
func canRetainMissingResellerChannel(prior ResellerModelConfig, authorized bool, ratio int64, configured, procurement bool) bool {
	if authorized && !prior.Authorized {
		return false
	}
	unchangedRate := ratio == prior.Ratio && configured == prior.hasConfiguredProcurementBaseRatio()
	if procurement {
		return unchangedRate || (ratio == ResellerRatioScale && !configured)
	}
	return unchangedRate && authorized == prior.Authorized
}
