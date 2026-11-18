package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetResellerObservation(view string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool("use_access_token") {
			resellerResponse(c, nil, model.ErrResellerForbidden)
			return
		}
		filter := model.ResellerObservationFilter{
			Model: c.Query("model"), RequestID: c.Query("request_id"),
			TaskID: c.Query("task_id"), Kind: c.Query("kind"), AfterEvent: c.Query("after_event"),
		}
		now := common.GetTimestamp()
		filter.From, filter.Until, filter.Page = now-7*86400, now+1, 1
		for key, target := range map[string]*int64{"from": &filter.From, "until": &filter.Until, "after_time": &filter.AfterTime} {
			if raw, present := c.GetQuery(key); present {
				value, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || value <= 0 {
					resellerResponse(c, nil, model.ErrResellerInvalid)
					return
				}
				*target = value
			}
		}
		for key, target := range map[string]*int{"customer_id": &filter.CustomerID, "page": &filter.Page} {
			if raw, present := c.GetQuery(key); present {
				value, err := strconv.Atoi(raw)
				if err != nil || value <= 0 {
					resellerResponse(c, nil, model.ErrResellerInvalid)
					return
				}
				*target = value
			}
		}
		if filter.Until <= filter.From || filter.Until > now+86400 || filter.Until-filter.From > 31*86400 ||
			filter.Page > 100000 || len(filter.Model) > 512 || len(filter.RequestID) > 64 || len(filter.TaskID) > 255 ||
			len(filter.AfterEvent) > 64 || (filter.AfterTime == 0) != (filter.AfterEvent == "") ||
			view == "usage" && filter.Kind != "" && filter.Kind != "consumption" && filter.Kind != "failure" ||
			view == "tasks" && filter.Kind != "" && filter.Kind != "drawing" && filter.Kind != "async" {
			resellerResponse(c, nil, model.ErrResellerInvalid)
			return
		}
		data, err := model.QueryResellerObservation(c.Request.Context(), c.GetInt("id"), view, filter)
		resellerResponse(c, data, err)
	}
}
