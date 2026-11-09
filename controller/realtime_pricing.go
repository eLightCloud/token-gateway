package controller

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetRealtimePricing(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	subject := c.Param("subject")
	var result gin.H
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		options = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.AuthorizeRealtimePricing(tx, c.GetInt("id"), subject, id); err != nil {
			return err
		}
		config, err := model.ReadRealtimePricing(tx, subject, id)
		if err != nil {
			return err
		}
		phase, err := model.RealtimePricingPhase(tx)
		if err != nil {
			return err
		}
		candidates, err := model.RealtimePricingCandidates(tx, c.GetInt("id"))
		if err != nil {
			return err
		}
		var history []model.RealtimePricingHistory
		if err := tx.Where("subject = ? AND subject_id = ?", subject, id).Order("revision DESC").Limit(51).Find(&history).Error; err != nil {
			return err
		}
		type historyEntry struct {
			model.RealtimePricingHistory
			Items         []model.RealtimePricingItem `json:"items"`
			PreviousItems []model.RealtimePricingItem `json:"previous_items"`
			OperatorName  string                      `json:"operator_name"`
		}
		entries := make([]historyEntry, 0, len(history))
		for i, h := range history {
			if i == 50 {
				break
			}
			var items []model.RealtimePricingItem
			if err := common.UnmarshalJsonStr(h.Snapshot, &items); err != nil {
				return err
			}
			var previous []model.RealtimePricingItem
			if i+1 < len(history) {
				if err := common.UnmarshalJsonStr(history[i+1].Snapshot, &previous); err != nil {
					return err
				}
			}
			entries = append(entries, historyEntry{RealtimePricingHistory: h, Items: items, PreviousItems: previous})
		}
		operatorIDs := make([]int, 0, len(entries))
		for _, entry := range entries {
			operatorIDs = append(operatorIDs, entry.OperatorID)
		}
		if len(operatorIDs) > 0 {
			var operators []model.User
			if err := tx.Select("id", "username").Where("id IN ?", operatorIDs).Find(&operators).Error; err != nil {
				return err
			}
			for i := range entries {
				for _, operator := range operators {
					if entries[i].OperatorID == operator.Id {
						entries[i].OperatorName = operator.Username
						break
					}
				}
			}
		}
		result = gin.H{"config": config, "phase": phase, "candidates": candidates, "history": entries}
		return nil
	}, options)
	resellerResponse(c, result, err)
}

func SaveRealtimePricing(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	var input model.RealtimePricingInput
	if err != nil || id <= 0 || common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20), &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	result, err := model.SaveRealtimePricing(c.GetInt("id"), c.Param("subject"), id, input)
	if err == nil {
		recordManageAuditFor(c, id, "realtime_pricing.save", map[string]any{"subject": c.Param("subject"), "revision": result.Revision})
	}
	resellerResponse(c, result, err)
}

func ChangeRealtimePricingMigration(c *gin.Context) {
	var input struct {
		Action string `json:"action"`
	}
	if common.DecodeJson(c.Request.Body, &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	conflicts, err := model.MigrateRealtimePricing(c.GetInt("id"), input.Action)
	if err == nil {
		recordManageAuditFor(c, 0, "realtime_pricing.migration", map[string]any{"action": input.Action, "conflicts": len(conflicts)})
	}
	phase, phaseErr := model.RealtimePricingPhase(model.DB)
	if err == nil {
		err = phaseErr
	}
	resellerResponse(c, gin.H{"phase": phase, "conflicts": conflicts}, err)
}

// Draft previews share the production resolver without persisting any draft.
func PreviewRealtimePricing(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	var input struct {
		Items []model.RealtimePricingItem `json:"items"`
	}
	if err != nil || id <= 0 || common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20), &input) != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	result, err := model.PreviewRealtimePricing(c.GetInt("id"), c.Param("subject"), id, input.Items)
	resellerResponse(c, result, err)
}
