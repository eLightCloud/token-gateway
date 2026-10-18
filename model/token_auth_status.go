package model

import "github.com/QuantumNous/new-api/common"

// A request may hold an old authentication snapshot while quota or status is
// changed. Only expire/exhaust a Token when the database still permits it.
func (token *Token) markAuthenticationUnavailable(status int) error {
	query := DB.Model(&Token{}).Where("id = ? AND status = ?", token.Id, common.TokenStatusEnabled)
	if status == common.TokenStatusExhausted {
		query = query.Where("unlimited_quota = ? AND remain_quota <= ?", false, 0)
	} else {
		query = query.Where("expired_time <> ? AND expired_time < ?", -1, common.GetTimestamp())
	}
	result := query.Update("status", status)
	if result.Error == nil && result.RowsAffected == 1 {
		token.Status = status
	}
	return result.Error
}
