package controller

import (
	"encoding/csv"
	"gorm.io/gorm"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func ListResellerUsers(c *gin.Context) {
	after, err := strconv.Atoi(c.DefaultQuery("after", "0"))
	if err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	data, err := model.ListResellerUsers(c.GetInt("id"), c.GetBool("reseller_admin"), after, c.Query("search"), c.Query("activation"), c.Query("service_status"), 50)
	resellerResponse(c, data, err)
}

func GetResellerWorkspace(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	admin := c.GetBool("reseller_admin")
	if err := model.ResellerManageTarget(model.DB, c.GetInt("id"), userID, admin); err != nil {
		resellerResponse(c, nil, err)
		return
	}
	config, err := model.ReadResellerConfig(model.DB, userID)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	var user model.User
	if err := model.DB.Select("id", "username", "email", "display_name", "quota", "status").First(&user, userID).Error; err != nil {
		resellerResponse(c, nil, err)
		return
	}
	identity, err := model.GetResellerIdentity(model.DB, userID)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	view := model.ResellerUserView{UserID: user.Id, Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Quota: user.Quota, PlatformDisabled: user.Status != common.UserStatusEnabled}
	if identity != nil {
		view.Activation = identity.Activation
		view.AgentDisabled = identity.AgentDisabled
	}
	if admin && identity != nil && identity.Kind == "agent" {
		var account model.ResellerAccount
		if err := model.DB.First(&account, "user_id = ?", userID).Error; err != nil {
			resellerResponse(c, nil, err)
			return
		}
		view.Account = &account
	}
	candidates := []model.ResellerConfigItem{}
	if admin {
		var channels []model.Channel
		if err := model.DB.Select("id", "models").Order("id").Find(&channels).Error; err != nil {
			resellerResponse(c, nil, err)
			return
		}
		for _, channel := range channels {
			for name := range strings.SplitSeq(channel.Models, ",") {
				if name != "" {
					candidates = append(candidates, model.ResellerConfigItem{Model: name, ChannelID: channel.Id, Ratio: "1"})
				}
			}
		}
	} else {
		parent, err := model.ReadResellerConfig(model.DB, c.GetInt("id"))
		if err != nil {
			resellerResponse(c, nil, err)
			return
		}
		for _, item := range parent.Items {
			if item.Authorized {
				item.Ratio = "1"
				candidates = append(candidates, item)
			}
		}
	}
	var history []model.ResellerConfigHistoryItem
	if err := model.DB.Model(&model.ResellerConfigRevision{}).Select("revision", "operator_id", "created_at").Where("user_id = ?", userID).Order("revision DESC").Limit(50).Find(&history).Error; err != nil {
		resellerResponse(c, nil, err)
		return
	}
	phase, err := model.RealtimePricingPhase(model.DB)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	resellerResponse(c, gin.H{"user": view, "config": config, "candidates": candidates, "history": history, "pricing_phase": phase}, nil)
}

func SaveResellerConfiguration(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var input model.ResellerConfigInput
	if err != nil || common.DecodeJson(c.Request.Body, &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	data, err := model.SaveResellerConfig(c.GetInt("id"), userID, c.GetBool("reseller_admin"), input)
	if err == nil {
		recordManageAuditFor(c, userID, "reseller.config.save", map[string]any{"revision": data.Revision})
	}
	resellerResponse(c, data, err)
}

func ChangeResellerEnabled(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err != nil || common.DecodeJson(c.Request.Body, &input) != nil || input.Enabled == nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	err = model.SetResellerEnabled(c.GetInt("id"), userID, c.GetBool("reseller_admin"), *input.Enabled)
	if err == nil {
		recordManageAuditFor(c, userID, "reseller.enabled.change", map[string]any{"enabled": *input.Enabled})
	}
	resellerResponse(c, nil, err)
}

func GetResellerAdjustments(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	after, parseErr := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil || parseErr != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	rows, err := model.ReadResellerAdjustments(c.GetInt("id"), userID, c.GetBool("reseller_admin"), after, 101)
	var next int64
	if len(rows) > 100 {
		rows = rows[:100]
		next = rows[len(rows)-1].ID
	}
	resellerResponse(c, gin.H{"items": rows, "next_cursor": next}, err)
}

func resellerBillingFilter(c *gin.Context) (model.ResellerBillingFilter, error) {
	values := []string{c.DefaultQuery("customer_id", "0"), c.DefaultQuery("after", "0"), c.DefaultQuery("from", "0"), c.DefaultQuery("until", strconv.FormatInt(common.GetTimestamp()+1, 10))}
	parsed := make([]int64, len(values))
	for i, value := range values {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return model.ResellerBillingFilter{}, model.ErrResellerInvalid
		}
		parsed[i] = number
	}
	var revision *int64
	if value, exists := c.GetQuery("projection_revision"); exists {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return model.ResellerBillingFilter{}, model.ErrResellerInvalid
		}
		revision = &parsed
	}
	return model.ResellerBillingFilter{RequestID: c.Query("request_id"), IncludeReservations: c.GetBool("reseller_procurement"), ProjectionRevision: revision, CustomerID: int(parsed[0]), After: parsed[1], From: parsed[2], Until: parsed[3], Model: c.Query("model"), Kind: c.Query("kind"), Posting: c.Query("posting"), Limit: 100}, nil
}
func GetResellerBilling(c *gin.Context) {
	filter, err := resellerBillingFilter(c)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	data, err := model.QueryResellerBilling(model.DB, c.GetInt("id"), filter)
	resellerResponse(c, data, err)
}

func ExportResellerBilling(c *gin.Context) {
	filter, err := resellerBillingFilter(c)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	resellerCSVExport(c, "reseller-billing.csv", func(tx *gorm.DB, writer *csv.Writer) error {
		if err := writer.Write([]string{"id", "request_id", "customer_id", "model", "channel_id", "kind", "base_quota", "customer_charge", "procurement_expense", "margin", "reserve_delta", "spend_delta", "occurred_at", "processed_at", "cutoff", "published_revision", "original_operation_id", "customer_ratio", "agent_ratio", "usage"}); err != nil {
			return err
		}
		for {
			page, err := model.QueryResellerBilling(tx, c.GetInt("id"), filter)
			if err != nil {
				return err
			}
			for _, row := range page.Items {
				usage, err := common.Marshal(row.Usage)
				if err != nil {
					return err
				}
				fields := []string{strconv.FormatInt(row.ID, 10), csvSafeResellerText(row.RequestID), strconv.Itoa(row.CustomerID), csvSafeResellerText(row.Model), strconv.Itoa(row.ChannelID), row.Kind, strconv.FormatInt(row.BaseQuota, 10), strconv.FormatInt(row.CustomerDelta, 10), strconv.FormatInt(row.SpendDelta, 10), resellerCSVQuota(row.Margin), strconv.FormatInt(row.ReserveDelta, 10), strconv.FormatInt(row.SpendDelta, 10), strconv.FormatInt(row.OccurredAt, 10), strconv.FormatInt(row.ProcessedAt, 10), strconv.FormatInt(filter.Until, 10), strconv.FormatInt(page.ProjectionRevision, 10), row.OriginalOperationID, row.CustomerRatio, row.AgentRatio, string(usage)}
				if row.Kind == "reserve" {
					fields[7] = ""
					fields[8] = ""
					fields[9] = ""
				}
				if err := writer.Write(fields); err != nil {
					return err
				}
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				return err
			}
			if c.Request.Context().Err() != nil {
				return c.Request.Context().Err()
			}
			if page.NextCursor == 0 {
				return nil
			}
			filter.After = page.NextCursor
		}
	})
}

func csvSafeResellerText(value string) string {
	if strings.HasPrefix(value, "=") || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "@") || strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	return value
}
func GetPersonalRatioRanges(c *gin.Context) {
	var user model.User
	if err := model.DB.First(&user, c.GetInt("id")).Error; err != nil {
		resellerResponse(c, nil, err)
		return
	}
	data, err := model.PersonalRatioRanges(user.Id, service.GetUserUsableGroups(user.Group))
	resellerResponse(c, data, err)
}

func PreviewResellerConfiguration(c *gin.Context) {
	userID, err := strconv.Atoi(c.Param("id"))
	var input model.ResellerConfigInput
	if err != nil || common.DecodeJson(c.Request.Body, &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	data, err := model.PreviewResellerConfig(c.GetInt("id"), userID, input)
	resellerResponse(c, data, err)
}

func GetPersonalRatioHistory(c *gin.Context) {
	before, err := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	if err != nil || before < 0 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	data, err := model.ReadPersonalRatioHistory(c.GetInt("id"), before, c.Query("cursor"))
	resellerResponse(c, data, err)
}

func resellerCSVQuota(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func GetResellerProcurement(c *gin.Context) {
	filter, err := resellerBillingFilter(c)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	data, err := model.QueryResellerProcurementAdjustments(model.DB, c.GetInt("id"), filter.After, filter.From, filter.Until, 100)
	resellerResponse(c, data, err)
}
func ExportResellerProcurement(c *gin.Context) {
	filter, err := resellerBillingFilter(c)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	resellerCSVExport(c, "reseller-procurement.csv", func(tx *gorm.DB, writer *csv.Writer) error {
		if err := writer.Write([]string{"id", "operation_id", "operator_id", "mode", "quota", "before", "after", "reason", "occurred_at", "cutoff"}); err != nil {
			return err
		}
		after := int64(0)
		for {
			page, err := model.QueryResellerProcurementAdjustments(tx, c.GetInt("id"), after, filter.From, filter.Until, 500)
			if err != nil {
				return err
			}
			for _, row := range page.Items {
				if err := writer.Write([]string{strconv.FormatInt(row.ID, 10), csvSafeResellerText(row.OperationID), strconv.Itoa(row.OperatorID), row.Mode, strconv.FormatInt(row.Quota, 10), strconv.FormatInt(row.Before, 10), strconv.FormatInt(row.After, 10), csvSafeResellerText(row.Reason), strconv.FormatInt(row.OccurredAt, 10), strconv.FormatInt(filter.Until, 10)}); err != nil {
					return err
				}
			}
			if c.Request.Context().Err() != nil {
				return c.Request.Context().Err()
			}
			if page.NextCursor == 0 {
				return nil
			}
			after = page.NextCursor
		}
	})
}
