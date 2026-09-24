package main

// refresh-organization-invoice 重建指定组织指定自然月的账单汇总（refresh 语义，
// 与服务端"重新生成"入口一致）。供运维在账期结束、数据补齐后定稿账单使用；
// OA 侧只读引用，不触发本命令。
//
// 用法示例：
//
//	go run ./cmd/refresh-organization-invoice -organization-id 1 -month 2026-08

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func main() {
	organizationId := flag.Int("organization-id", 0, "organization ID")
	month := flag.String("month", "", "natural month in YYYY-MM (Asia/Shanghai)")
	waitSeconds := flag.Int("wait", 180, "seconds to wait for the async build")
	flag.Parse()

	if err := run(*organizationId, *month, *waitSeconds); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(organizationId int, month string, waitSeconds int) error {
	if organizationId <= 0 || len(month) != 7 {
		return errors.New("organization-id and month (YYYY-MM) are required")
	}
	now := time.Now()
	periodStart := month + "-01"
	periodEndDate := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).
		AddDate(0, 1, -1).Format("2006-01-02")
	if t, err := time.ParseInLocation("2006-01", month, time.Local); err == nil {
		periodEndDate = t.AddDate(0, 1, 0).AddDate(0, 0, -1).Format("2006-01-02")
	}
	period, err := model.NewOrganizationInvoicePeriod(periodStart, periodEndDate, now)
	if err != nil {
		return err
	}

	common.InitEnv()
	common.IsMasterNode = false
	if err := model.InitDB(); err != nil {
		return err
	}

	// refresh=true：失效既有汇总并按当前数据重建（与控制台"重新生成"一致）。
	_, err = service.GetOrganizationInvoice(organizationId, period, true)
	if err != nil {
		return fmt.Errorf("enqueue refresh: %w", err)
	}

	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
	for {
		time.Sleep(2 * time.Second)
		var rows []model.OrganizationInvoicePeriodSummary
		if err := model.DB.Where(
			"organization_id = ? AND period_start = ? AND period_end = ? AND calculation_version = ?",
			organizationId, period.StartTimestamp, period.EndTimestamp,
			model.OrganizationInvoiceSummaryCalculationVersion,
		).Order("revision DESC").Limit(1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			if time.Now().After(deadline) {
				return errors.New("summary row not found before timeout")
			}
			continue
		}
		summary := rows[0]
		fmt.Printf("revision=%d status=%s finalized=%t source_as_of=%s\n",
			summary.Revision, summary.Status, summary.Finalized,
			time.Unix(summary.SourceAsOf, 0).Format(time.RFC3339))
		switch summary.Status {
		case model.OrganizationInvoiceSummaryStatusReady:
			fmt.Println("refresh complete")
			return nil
		case model.OrganizationInvoiceSummaryStatusFailed:
			return fmt.Errorf("build failed: %s", summary.Error)
		}
		if time.Now().After(deadline) {
			return errors.New("build did not finish before timeout")
		}
	}
}
