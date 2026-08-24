package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var (
	ErrBillingWalletQuotaInsufficient = errors.New("wallet quota is insufficient")
	ErrBillingTokenQuotaInsufficient  = errors.New("token quota is insufficient")
)

type BillingBalancePreConsumeParams struct {
	RequestId      string
	UserId         int
	ModelName      string
	Amount         int64
	BillingSource  string
	TokenId        int
	ApplyToken     bool
	TokenUnlimited bool
}

// PreConsumeBillingBalances debits the selected funding source and token in
// one main-database transaction. It is the request-admission boundary: neither
// balance can be committed without the other.
func PreConsumeBillingBalances(params BillingBalancePreConsumeParams) (*SubscriptionPreConsumeResult, error) {
	if params.UserId <= 0 || params.Amount < 0 {
		return nil, errors.New("invalid billing pre-consume params")
	}
	var subscription *SubscriptionPreConsumeResult
	err := DB.Transaction(func(tx *gorm.DB) error {
		if params.Amount > 0 {
			switch params.BillingSource {
			case "subscription":
				var err error
				subscription, err = PreConsumeUserSubscription(params.RequestId, params.UserId, params.ModelName, 0, params.Amount, tx)
				if err != nil {
					return err
				}
			case "wallet":
				result := tx.Model(&User{}).
					Where("id = ? AND quota >= ?", params.UserId, params.Amount).
					Update("quota", gorm.Expr("quota - ?", params.Amount))
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return fmt.Errorf("%w for user %d", ErrBillingWalletQuotaInsufficient, params.UserId)
				}
			default:
				return fmt.Errorf("unsupported billing source %q", params.BillingSource)
			}
		}

		return preConsumeTokenBalanceTx(tx, params.TokenId, params.Amount, params.ApplyToken, params.TokenUnlimited)
	})
	if err != nil {
		return nil, err
	}
	invalidateBillingBalanceCaches(params.UserId, params.TokenId, params.BillingSource, params.ApplyToken)
	return subscription, nil
}

type BillingBalanceReserveParams struct {
	UserId         int
	Amount         int64
	BillingSource  string
	SubscriptionId int
	TokenId        int
	ApplyToken     bool
	TokenUnlimited bool
}

// ReserveBillingBalances extends an existing pre-consume atomically. Wallet
// reserve keeps the established behavior of allowing debt; subscription and
// limited-token bounds remain enforced by their existing rules.
func ReserveBillingBalances(params BillingBalanceReserveParams) error {
	if params.UserId <= 0 || params.Amount <= 0 {
		return errors.New("invalid billing reserve params")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		switch params.BillingSource {
		case "subscription":
			if err := postConsumeUserSubscriptionDeltaTx(tx, params.SubscriptionId, params.Amount); err != nil {
				return err
			}
		case "wallet":
			if err := adjustUserWalletInTx(tx, params.UserId, -params.Amount); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported billing source %q", params.BillingSource)
		}
		return preConsumeTokenBalanceTx(tx, params.TokenId, params.Amount, params.ApplyToken, params.TokenUnlimited)
	})
	if err != nil {
		return err
	}
	invalidateBillingBalanceCaches(params.UserId, params.TokenId, params.BillingSource, params.ApplyToken)
	return nil
}

func preConsumeTokenBalanceTx(tx *gorm.DB, tokenId int, amount int64, apply bool, unlimited bool) error {
	if !apply || amount == 0 {
		return nil
	}
	if tokenId <= 0 {
		return errors.New("invalid billing token id")
	}
	query := tx.Model(&Token{}).Where("id = ?", tokenId)
	if !unlimited {
		query = query.Where("remain_quota >= ?", amount)
	}
	result := query.Updates(map[string]interface{}{
		"remain_quota":  gorm.Expr("remain_quota - ?", amount),
		"used_quota":    gorm.Expr("used_quota + ?", amount),
		"accessed_time": common.GetTimestamp(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w for token %d", ErrBillingTokenQuotaInsufficient, tokenId)
	}
	return nil
}

func invalidateBillingBalanceCaches(userId int, tokenId int, billingSource string, applyToken bool) {
	if billingSource != "subscription" {
		if err := invalidateUserCache(userId); err != nil {
			common.SysLog("failed to invalidate pre-consume user cache: " + err.Error())
		}
	}
	if !common.RedisEnabled || !applyToken || tokenId <= 0 {
		return
	}
	var token Token
	if err := DB.Select("key").Where("id = ?", tokenId).First(&token).Error; err == nil && token.Key != "" {
		if err := cacheDeleteToken(token.Key); err != nil {
			common.SysLog("failed to invalidate pre-consume token cache: " + err.Error())
		}
	}
}
