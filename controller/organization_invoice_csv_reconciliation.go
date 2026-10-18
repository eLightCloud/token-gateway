package controller

import (
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/model"
)

// Reconciliation metadata belongs to the exported snapshot as well as the UI.
// Only unresolved evidence is listed; normal replay and refunds are not errors.
func writeOrganizationInvoiceReconciliationCSV(writer *csv.Writer, invoice *model.OrganizationInvoice) error {
	review := organizationInvoiceCSVReviewFor(invoice)
	names := make(map[int]string, len(invoice.Accounts))
	differences := make(map[int]string, len(invoice.Accounts))
	for _, account := range invoice.Accounts {
		names[account.UserId] = account.Username
		if value := account.Financials.ReconciliationDifferenceAmountUSD; value != nil && *value != "" {
			differences[account.UserId] = *value
		}
	}
	for _, group := range []struct {
		title  string
		issues []model.OrganizationInvoiceExpenseIssue
		wallet bool
	}{
		{"# 费用待确认", review.expenseIssues, false},
		{"# 钱包收支待核对", review.walletIssues, true},
	} {
		if len(group.issues) == 0 {
			continue
		}
		for _, row := range [][]string{{}, {group.title}, {"账号", "任务", "原因"}} {
			if err := writer.Write(row); err != nil {
				return err
			}
		}
		if group.wallet && !review.expenseNeedsReview {
			if err := writer.Write([]string{"说明", "钱包问题不改变当前已确认消费金额"}); err != nil {
				return err
			}
		}
		for _, issue := range group.issues {
			name := ""
			if issue.UserId > 0 {
				name = names[issue.UserId]
				if name == "" {
					name = strconv.Itoa(issue.UserId)
				}
			}
			reason := organizationInvoiceCSVReviewReason(issue.Reason)
			if group.wallet && differences[issue.UserId] != "" {
				formatted, err := invoiceCSVAmount(differences[issue.UserId])
				if err != nil {
					return err
				}
				reason = fmt.Sprintf("%s；已有核对差额：%s", reason, formatted)
			}
			if err := writer.Write([]string{name, issue.TaskId, reason}); err != nil {
				return err
			}
		}
		if !group.wallet {
			for _, count := range []struct {
				label string
				value int64
			}{
				{"待确认任务数", invoice.ExpenseCoverage.PendingTasks},
				{"未分类记录数", invoice.ExpenseCoverage.UnknownRows},
				{"待投递消费记录", invoice.ExpenseCoverage.PendingSourceRows},
			} {
				if count.value > 0 {
					if err := writer.Write([]string{count.label, strconv.FormatInt(count.value, 10)}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
