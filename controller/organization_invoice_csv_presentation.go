package controller

import (
	"encoding/csv"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

type organizationInvoiceCSVReview struct {
	expenseNeedsReview bool
	expenseIssues      []model.OrganizationInvoiceExpenseIssue
	walletIssues       []model.OrganizationInvoiceExpenseIssue
}

// Derive display decisions from the persisted snapshot only. In particular,
// missing coverage is not evidence of a complete or zero bill.
func organizationInvoiceCSVReviewFor(invoice *model.OrganizationInvoice) organizationInvoiceCSVReview {
	c := invoice.ExpenseCoverage
	review := organizationInvoiceCSVReview{
		expenseNeedsReview: c.Status != "complete" || c.PendingTasks > 0 || c.UnknownRows > 0 || c.PendingSourceRows > 0 || len(c.Issues) > 0,
	}
	type issueKey struct {
		user         int
		task, reason string
	}
	for _, group := range []struct {
		input  []model.OrganizationInvoiceExpenseIssue
		output *[]model.OrganizationInvoiceExpenseIssue
	}{{c.Issues, &review.expenseIssues}, {c.WalletIssues, &review.walletIssues}} {
		seen := make(map[issueKey]bool, len(group.input))
		for _, issue := range group.input {
			key := issueKey{issue.UserId, issue.TaskId, issue.Reason}
			if !seen[key] {
				*group.output = append(*group.output, issue)
				seen[key] = true
			}
		}
	}
	if review.expenseNeedsReview && len(review.expenseIssues) == 0 {
		reason := "expense_evidence_incomplete"
		if c.Status == "" {
			reason = "expense_coverage_missing"
		}
		review.expenseIssues = append(review.expenseIssues, model.OrganizationInvoiceExpenseIssue{Reason: reason})
	}
	walletUsers := make(map[int]bool, len(review.walletIssues))
	hasWalletWarning := false
	for _, issue := range review.walletIssues {
		walletUsers[issue.UserId] = true
		if issue.UserId == 0 && issue.TaskId == "" && issue.Reason == "wallet_information_unavailable" {
			hasWalletWarning = true
		}
	}
	for _, account := range invoice.Accounts {
		status := account.Financials.ReconciliationStatus
		if walletUsers[account.UserId] || (status != "incomplete" && status != "unavailable") {
			continue
		}
		reason := "wallet_evidence_incomplete"
		if status == "unavailable" {
			reason = "wallet_information_unavailable"
		}
		review.walletIssues = append(review.walletIssues, model.OrganizationInvoiceExpenseIssue{UserId: account.UserId, Reason: reason})
	}
	if invoice.WalletWarning != "" && !hasWalletWarning {
		// Never expose raw SQL/connection diagnostics in the customer export.
		review.walletIssues = append(review.walletIssues, model.OrganizationInvoiceExpenseIssue{Reason: "wallet_information_unavailable"})
	}
	return review
}

func organizationInvoiceCSVReviewReason(reason string) string {
	switch reason {
	case "final_expense_evidence_missing":
		return "最终费用尚未确认"
	case "expense_origin_missing":
		return "缺少消费归属依据"
	case "consume_log_pending":
		return "消费记录待投递"
	case "consume_history_cleared":
		return "历史消费记录已清理"
	case "log_source_snapshot_unverified":
		return "消费来源快照待核对"
	case "consume_logging_disabled":
		return "消费日志未启用"
	case "expense_coverage_missing":
		return "消费完整性依据不足"
	case "expense_evidence_incomplete":
		return "部分消费依据仍待核对"
	case "task_funding_pending":
		return "任务资金处理在途"
	case "original_task_charge_missing":
		return "缺少任务原扣款记录"
	case "task_cash_flow_mismatch":
		return "任务扣款与退款记录不一致"
	case "wallet_information_unavailable":
		return "钱包信息暂不可用"
	case "wallet_evidence_incomplete":
		return "钱包收支依据不足"
	default:
		return "核对依据不足"
	}
}

func writeOrganizationInvoiceCSVHeaderNotes(writer *csv.Writer, invoice *model.OrganizationInvoice) error {
	p := invoice.CoveragePeriod
	coverage := []string{"实际覆盖", "实际覆盖依据不足"}
	if p.StartDate != "" && p.EndDate != "" {
		start, end := p.StartDate, p.EndDate
		location := time.FixedZone(model.OrganizationInvoiceTimezone, 8*60*60)
		if p.StartTimestamp > 0 {
			at := time.Unix(p.StartTimestamp, 0).In(location)
			if at.Hour() != 0 || at.Minute() != 0 || at.Second() != 0 {
				start = at.Format("2006-01-02 15:04:05")
			}
		}
		if p.EndTimestamp > 0 {
			at := time.Unix(p.EndTimestamp, 0).In(location)
			if at.Hour() != 23 || at.Minute() != 59 || at.Second() != 59 {
				end = at.Format("2006-01-02 15:04:05")
			}
		}
		coverage = []string{"实际覆盖", start, end}
	}
	if err := writer.Write(coverage); err != nil {
		return err
	}
	if organizationInvoiceCSVReviewFor(invoice).expenseNeedsReview {
		if err := writer.Write([]string{"金额说明", "已确认消费小计，仍有费用待核对"}); err != nil {
			return err
		}
		return writer.Write([]string{"结算说明", "结算金额仅覆盖已确认消费部分"})
	}
	return nil
}

// A single optional row follows the existing account financial rows. Refunds
// are already part of inflow; this is a breakdown, never a second addition.
func writeOrganizationInvoiceCSVRefunds(writer *csv.Writer, invoice *model.OrganizationInvoice, formatter *organizationInvoiceCSVAmountFormatter) error {
	row := []string{"其中退款（已含在当期流入中）"}
	anyRefund := false
	for _, account := range invoice.Accounts {
		value := account.Financials.WalletRefundAmountUSD
		if value == "" {
			row = append(row, "")
			continue
		}
		amount, err := decimal.NewFromString(value)
		if err != nil {
			return err
		}
		if amount.IsZero() {
			row = append(row, "")
			continue
		}
		anyRefund = true
		row = append(row, formatter.amount(value))
	}
	if !anyRefund {
		return nil
	}
	row = append(row, "", "", "")
	return writer.Write(row)
}
