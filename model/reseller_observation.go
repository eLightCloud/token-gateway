package model

import (
	"context"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ResellerObservationFilter struct {
	CustomerID int
	From       int64
	Until      int64
	Model      string
	RequestID  string
	TaskID     string
	Kind       string
	Page       int
	AfterTime  int64
	AfterEvent string
}

type ResellerObservationCustomer struct {
	UserID   int    `json:"user_id"`
	Username string `json:"username"`
}

type resellerObservationOverview struct {
	Customers int64 `json:"customers"`
	Awaiting  int64 `json:"awaiting"`
	Active    int64 `json:"active"`
	Requests  int64 `json:"requests"`
	Failures  int64 `json:"failures"`
}

type resellerObservationPoint struct {
	DayStart int64  `json:"day_start"`
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

type resellerUsageRow struct {
	ResellerObservationCustomer
	CreatedAt        int64  `json:"created_at"`
	ModelName        string `json:"model_name"`
	Type             int    `json:"type"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	UseTime          int    `json:"use_time"`
	RequestID        string `json:"request_id"`
}

type resellerTaskRow struct {
	ResellerObservationCustomer
	TaskID     string `json:"task_id"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Progress   string `json:"progress"`
	SubmitTime int64  `json:"submit_time"`
	StartTime  int64  `json:"start_time"`
	FinishTime int64  `json:"finish_time"`
}

type resellerAuditRow struct {
	ResellerObservationCustomer
	CreatedAt int64  `json:"created_at"`
	EventID   string `json:"event_id"`
	Operator  string `json:"operator"`
	Action    string `json:"action"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Quota     int64  `json:"quota,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
}

const resellerObservationPageSize = 25

type resellerObservationPage[T any] struct {
	Items   []T  `json:"items"`
	HasMore bool `json:"has_more"`
}

func resellerObservationPageOf[T any](rows []T) resellerObservationPage[T] {
	return resellerObservationPage[T]{
		Items: rows[:min(len(rows), resellerObservationPageSize)], HasMore: len(rows) > resellerObservationPageSize,
	}
}

// QueryResellerObservation is called after the session and security-completion gate.
func QueryResellerObservation(ctx context.Context, operatorID int, view string, filter ResellerObservationFilter) (any, error) {
	db := DB.WithContext(ctx)
	identity, err := GetResellerIdentity(db, operatorID)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.Kind != "agent" || identity.Activation != "active" {
		return nil, ErrResellerForbidden
	}
	customers := db.Model(&ResellerIdentity{}).Where("kind = ? AND agent_user_id = ?", "agent_customer", operatorID)
	if filter.CustomerID != 0 {
		customers = customers.Where("user_id = ?", filter.CustomerID)
		var customer ResellerObservationCustomer
		if err := customers.Session(&gorm.Session{}).Select("user_id").Take(&customer).Error; err != nil {
			return nil, err
		}
	}
	customerIDs := customers.Session(&gorm.Session{}).Select("user_id")
	if view == "customers" {
		rows := make([]ResellerObservationCustomer, 0)
		err := db.Model(&User{}).Select("id AS user_id, username").Where("id IN (?)", customerIDs).Order("username, id").Find(&rows).Error
		return rows, err
	}
	if view == "tasks" {
		return queryResellerObservationTasks(db, customerIDs, filter)
	}

	logDB := LOG_DB.WithContext(ctx)
	if view == "audit" {
		return queryResellerObservationAudit(logDB, customerIDs, operatorID, filter)
	}
	logs := logDB.Table("logs").
		Where("created_at >= ? AND created_at < ?", filter.From, filter.Until).
		Where("type IN ?", []int{LogTypeConsume, LogTypeError})
	if DB == LOG_DB {
		logs = logs.Where("user_id IN (?)", customerIDs)
	} else {
		var ids []int
		if err := customerIDs.Pluck("user_id", &ids).Error; err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			logs = logs.Where("1 = 0")
		} else {
			// These integer literals originate in the main database. Building the
			// list here avoids per-engine bind limits on large customer accounts.
			var list strings.Builder
			for i, id := range ids {
				if i > 0 {
					list.WriteByte(',')
				}
				list.WriteString(strconv.Itoa(id))
			}
			logs = logs.Where("user_id IN (" + list.String() + ")")
		}
	}
	if filter.Model != "" {
		logs = logs.Where("model_name = ?", filter.Model)
	}

	switch view {
	case "overview":
		var overview resellerObservationOverview
		var accounts struct{ Customers, Awaiting int64 }
		if err := customers.Select("COUNT(*) AS customers, COUNT(CASE WHEN awaiting_enablement = ? THEN 1 END) AS awaiting", true).Scan(&accounts).Error; err != nil {
			return nil, err
		}
		err := logs.Select(`COUNT(*) AS requests,
			COUNT(CASE WHEN type = ? THEN 1 END) AS failures,
			COUNT(DISTINCT CASE WHEN type = ? THEN user_id END) AS active`, LogTypeError, LogTypeConsume).Scan(&overview).Error
		overview.Customers, overview.Awaiting = accounts.Customers, accounts.Awaiting
		return overview, err
	case "analytics":
		rows := make([]resellerObservationPoint, 0)
		err := logs.Select(`created_at - (created_at % 86400) AS day_start, model_name AS model,
			COUNT(*) AS requests, COALESCE(SUM(prompt_tokens), 0) + COALESCE(SUM(completion_tokens), 0) AS tokens`).
			Group("day_start, model_name").Order("day_start, model_name").Scan(&rows).Error
		return rows, err
	case "usage":
		if filter.RequestID != "" {
			logs = logs.Where("request_id = ?", filter.RequestID)
		}
		switch filter.Kind {
		case "consumption":
			logs = logs.Where("type = ?", LogTypeConsume)
		case "failure":
			logs = logs.Where("type = ?", LogTypeError)
		}
		order := "created_at DESC, id DESC"
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			order = clickHouseLogOrder("") + ", user_id DESC, type DESC"
		}
		rows := make([]resellerUsageRow, 0)
		err := logs.Select("user_id, username, created_at, model_name, type, prompt_tokens, completion_tokens, use_time, request_id").
			Order(order).Offset((filter.Page - 1) * resellerObservationPageSize).Limit(resellerObservationPageSize + 1).Scan(&rows).Error
		return resellerObservationPageOf(rows), err
	default:
		return nil, ErrResellerInvalid
	}
}

func queryResellerObservationTasks(db, customerIDs *gorm.DB, filter ResellerObservationFilter) (any, error) {
	table, id := "tasks", "task_id"
	if filter.Kind == "drawing" {
		table, id = "midjourneys", "mj_id"
	}
	query := db.Table(table+" AS task").Joins("LEFT JOIN users ON users.id = task.user_id").
		Where("task.user_id IN (?) AND task.submit_time >= ? AND task.submit_time < ?", customerIDs, filter.From, filter.Until)
	if filter.TaskID != "" {
		query = query.Where("task."+id+" = ?", filter.TaskID)
	}
	rows := make([]resellerTaskRow, 0)
	err := query.Select("task.user_id, users.username, task." + id + " AS task_id, task.action, task.status, task.progress, task.submit_time, task.start_time, task.finish_time").
		Order("task.submit_time DESC, task.id DESC").Offset((filter.Page - 1) * resellerObservationPageSize).
		Limit(resellerObservationPageSize + 1).Scan(&rows).Error
	for i := range rows {
		progress, err := strconv.Atoi(strings.TrimSuffix(rows[i].Progress, "%"))
		if err != nil || progress < 0 || progress > 100 {
			rows[i].Progress = ""
		} else {
			rows[i].Progress = strconv.Itoa(progress) + "%"
		}
	}
	return resellerObservationPageOf(rows), err
}

var resellerCustomerAuditActions = []string{
	"reseller.customer.create", "reseller.enabled.change", "reseller.quota.adjust", "reseller.config.save",
}

func queryResellerObservationAudit(logDB, customerIDs *gorm.DB, operatorID int, filter ResellerObservationFilter) (any, error) {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		logDB = logDB.WithContext(clickhouse.Context(logDB.Statement.Context, clickhouse.WithSettings(clickhouse.Settings{
			"output_format_native_write_json_as_string": 1,
			"output_format_json_quote_64bit_integers":   0,
		})))
	}
	query := logDB.Model(&AuditLog{}).Select("event_id, created_at, username, action, other").
		Where("user_id = ? AND actor_role = ? AND category = ? AND action IN ?", operatorID, common.RoleCommonUser, AuditCategoryOperation, resellerCustomerAuditActions).
		Where("created_at >= ? AND created_at < ?", filter.From, filter.Until)
	rows := make([]resellerAuditRow, 0)
	afterTime, afterEvent := filter.AfterTime, filter.AfterEvent
	const batchSize = 100
	for len(rows) <= resellerObservationPageSize {
		batch := make([]AuditLog, 0, batchSize)
		page := query.Session(&gorm.Session{})
		if afterEvent != "" {
			page = page.Where("created_at < ? OR (created_at = ? AND event_id < ?)", afterTime, afterTime, afterEvent)
		}
		if err := page.Order("created_at DESC, event_id DESC").Limit(batchSize).Find(&batch).Error; err != nil {
			return nil, err
		}
		candidates := make([]resellerAuditRow, 0, len(batch))
		ids := make([]int, 0, len(batch))
		for _, event := range batch {
			op := event.Other.Op
			if op == nil || op.Action != event.Action {
				continue
			}
			row := resellerAuditRow{CreatedAt: event.CreatedAt, EventID: event.EventId, Operator: event.Username, Action: event.Action}
			if !resellerAuditParam(op, "target_user_id", &row.UserID) || row.UserID <= 0 {
				continue
			}
			switch row.Action {
			case "reseller.enabled.change":
				resellerAuditParam(op, "enabled", &row.Enabled)
			case "reseller.quota.adjust":
				resellerAuditParam(op, "quota", &row.Quota)
				resellerAuditParam(op, "mode", &row.Mode)
				if row.Mode != "add" && row.Mode != "subtract" {
					row.Mode = ""
				}
			case "reseller.config.save":
				resellerAuditParam(op, "revision", &row.Revision)
			}
			candidates = append(candidates, row)
			ids = append(ids, row.UserID)
		}
		if len(ids) > 0 {
			var customers []ResellerObservationCustomer
			err := DB.WithContext(logDB.Statement.Context).Model(&User{}).Select("id AS user_id, username").
				Where("id IN (?) AND id IN ?", customerIDs, ids).Find(&customers).Error
			if err != nil {
				return nil, err
			}
			names := make(map[int]string, len(customers))
			for _, customer := range customers {
				names[customer.UserID] = customer.Username
			}
			for _, candidate := range candidates {
				if name, ok := names[candidate.UserID]; ok {
					candidate.Username = name
					rows = append(rows, candidate)
				}
			}
		}
		if len(batch) < batchSize {
			break
		}
		last := batch[len(batch)-1]
		afterTime, afterEvent = last.CreatedAt, last.EventId
	}
	return resellerObservationPageOf(rows), nil
}

func resellerAuditParam(op *AuditOperation, key string, target any) bool {
	encoded, err := common.Marshal(op.Params[key])
	return err == nil && common.Unmarshal(encoded, target) == nil
}
