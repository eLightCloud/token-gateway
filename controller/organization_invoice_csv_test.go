package controller

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteOrganizationInvoiceCSVUsesConciseAccountFinancialRows(t *testing.T) {
	invoice := &model.OrganizationInvoice{
		ExpenseCoverage: model.OrganizationInvoiceExpenseCoverage{Status: "complete"},
		Period: model.OrganizationInvoicePeriod{
			StartDate:      "2026-07-01",
			EndDate:        "2026-07-31",
			Timezone:       model.OrganizationInvoiceTimezone,
			StartTimestamp: 1_783_353_600,
			EndTimestamp:   1_786_032_000,
		},
		Currency: "USD",
		Accounts: []model.OrganizationInvoiceAccount{
			{
				UserId: 11, Username: "alice", DisplayName: "A***e", GrossAmountUSD: "1.25",
				Financials: model.OrganizationInvoiceAccountFinancials{
					OpeningBalanceAmountUSD: "4", PaymentTopUpAmountUSD: "10", AdminIncreaseAmountUSD: "2.5",
					TotalInflowAmountUSD: "12.5", CurrentBalanceAmountUSD: "6.25",
				},
			},
			{
				UserId: 12, Username: "bob", GrossAmountUSD: "0.75",
				Financials: model.OrganizationInvoiceAccountFinancials{
					OpeningBalanceAmountUSD: "2.5", PaymentTopUpAmountUSD: "0", AdminIncreaseAmountUSD: "3",
					TotalInflowAmountUSD: "3", CurrentBalanceAmountUSD: "1.5",
				},
			},
		},
		CategoryRows: []model.OrganizationInvoiceCategoryRow{{
			CategoryName: "GPT",
			AccountAmounts: []model.OrganizationInvoiceAccountAmount{
				{UserId: 11, GrossAmountUSD: "1.25"},
				{UserId: 12, GrossAmountUSD: "0.75"},
			},
			GrossQuota:       1_000_000,
			GrossAmountUSD:   "2",
			Factor:           "0.9900",
			SettledAmountUSD: "1.98",
		}},
		ModelRows: []model.OrganizationInvoiceModelRow{{
			ModelName: "gpt-4o",
			AccountAmounts: []model.OrganizationInvoiceAccountAmount{
				{UserId: 11, GrossAmountUSD: "1.25"},
				{UserId: 12, GrossAmountUSD: "0.75"},
			},
			GrossQuota:     1_000_000,
			GrossAmountUSD: "2",
			SharePercent:   "100.0",
		}},
		GrossTotalQuota:       1_000_000,
		GrossTotalAmountUSD:   "2",
		SettledTotalAmountUSD: "1.98",
	}
	exportContext := &model.OrganizationInvoiceExportContext{
		OrganizationName: "中关村学院",
		AccountDisplayNames: map[int]string{
			11: "张宇",
		},
	}

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writeOrganizationInvoiceCSV(writer, exportContext, invoice))
	writer.Flush()
	require.NoError(t, writer.Error())

	reader := csv.NewReader(strings.NewReader(buffer.String()))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	require.NoError(t, err)
	// The additive header notes may change row positions, not the matrix contract.
	rows := make(map[string][]string)
	for _, record := range records {
		if len(record) > 0 {
			rows[record[0]] = record
		}
	}
	assert.Equal(t, []string{"组织名称", "中关村学院"}, records[0])
	assert.Equal(t, []string{"账期", "2026-07-01 ~ 2026-07-31"}, records[1])
	assert.Equal(t, []string{"币种", "USD"}, records[2])
	assert.Contains(t, records, []string{"真实姓名", "张宇", "", "", "", ""})
	assert.Equal(t, []string{"模型类别", "alice", "bob", "折前合计", "结算系数", "结算后金额"}, rows["模型类别"])
	assert.Equal(t, []string{"用户上期余额", "4.000000", "2.500000", "", "", ""}, rows["用户上期余额"])
	assert.Equal(t, []string{"用户当期流入金额", "12.500000", "3.000000", "", "", ""}, rows["用户当期流入金额"])
	assert.Equal(t, []string{"用户当期消费金额", "1.250000", "0.750000", "", "", ""}, rows["用户当期消费金额"])
	assert.Equal(t, []string{"用户当前余额", "6.250000", "1.500000", "", "", ""}, rows["用户当前余额"])
	assert.Contains(t, records, []string{"真实姓名", "张宇", "", "", ""})
	assert.Equal(t, []string{"模型", "alice", "bob", "合计", "占比"}, rows["模型"])

	exported := buffer.String()
	assert.NotContains(t, exported, "组织 ID")
	assert.NotContains(t, exported, "时区")
	assert.NotContains(t, exported, "时间戳")
	assert.NotContains(t, exported, "规则明细")
	assert.NotContains(t, exported, "quota")
	assert.NotContains(t, exported, "A***e")
	assert.NotContains(t, exported, "张宇（alice）")
	assert.NotContains(t, exported, "用户期初余额")
	assert.NotContains(t, exported, "用户当期支付充值金额")
	assert.NotContains(t, exported, "用户当期管理员增加金额")
	assert.NotContains(t, exported, "用户当期全部入账金额")
	assert.NotContains(t, exported, "用户当期全部扣减金额")
	assert.NotContains(t, exported, "余额闭合状态")
}

func TestWriteOrganizationInvoiceCSVKeepsSubtotalAndUnknownWalletExplicit(t *testing.T) {
	invoice := &model.OrganizationInvoice{
		Period: model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-30"}, Currency: "USD", EvidenceAsOf: 1790000000,
		CoveragePeriod:      model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-15"},
		ExpenseCoverage:     model.OrganizationInvoiceExpenseCoverage{Status: "incomplete", PendingTasks: 1, PendingSourceRows: 1, WalletIssues: []model.OrganizationInvoiceExpenseIssue{{UserId: 91, TaskId: "funding-pending", Reason: "task_funding_pending"}}, Issues: []model.OrganizationInvoiceExpenseIssue{{UserId: 91, TaskId: "task-pending", Reason: "final_expense_evidence_missing"}, {Reason: "consume_history_cleared"}}},
		Accounts:            []model.OrganizationInvoiceAccount{{UserId: 91, Username: "account-91", GrossAmountUSD: "0.347618", Financials: model.OrganizationInvoiceAccountFinancials{ReconciliationStatus: "unavailable"}}},
		GrossTotalAmountUSD: "0.347618", SettledTotalAmountUSD: "0.347618",
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writeOrganizationInvoiceCSV(writer, &model.OrganizationInvoiceExportContext{OrganizationName: "reconciliation"}, invoice))
	writer.Flush()
	require.NoError(t, writer.Error())
	assert.Contains(t, buffer.String(), "0.347618")
	assert.Contains(t, buffer.String(), "已确认消费小计，仍有费用待核对")
	assert.Contains(t, buffer.String(), "最终费用尚未确认")
	assert.Contains(t, buffer.String(), "task-pending")
	assert.Contains(t, buffer.String(), "# 钱包收支待核对")
	assert.Contains(t, buffer.String(), "实际覆盖,2026-09-01,2026-09-15")
	assert.Contains(t, buffer.String(), "任务资金处理在途")
	assert.Contains(t, buffer.String(), "待投递消费记录,1")
	assert.Contains(t, buffer.String(), "历史消费记录已清理")
	assert.Contains(t, buffer.String(), "用户上期余额,—")
	assert.NotContains(t, buffer.String(), "用户上期余额,0.000000")
}

func TestWriteOrganizationInvoiceCSVOnlyListsUnresolvedProblems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		pending int64
		problem bool
		refund  string
		wallet  string
	}{
		{name: "complete", status: "complete"},
		{name: "missing_coverage", problem: true},
		{name: "incomplete_without_details", status: "incomplete", problem: true},
		{name: "contradictory_counter", status: "complete", pending: 1, problem: true},
		{name: "normal_refund", status: "complete", refund: "80"},
		{name: "wallet_only_gap", status: "complete", wallet: "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoice := &model.OrganizationInvoice{
				Period:         model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-30"},
				CoveragePeriod: model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-30"},
				Currency:       "USD", GrossTotalAmountUSD: "20", SettledTotalAmountUSD: "18",
				ExpenseCoverage: model.OrganizationInvoiceExpenseCoverage{Status: tc.status, PendingSourceRows: tc.pending},
				Accounts:        []model.OrganizationInvoiceAccount{{UserId: 91, Username: "alice", GrossAmountUSD: "20", Financials: model.OrganizationInvoiceAccountFinancials{ReconciliationStatus: "derived", WalletRefundAmountUSD: "0.0000000000"}}},
			}
			if tc.refund != "" {
				invoice.Accounts[0].Financials.WalletRefundAmountUSD = tc.refund
			}
			if tc.wallet != "" {
				invoice.Accounts[0].Financials.ReconciliationStatus = tc.wallet
			}
			var buffer bytes.Buffer
			writer := csv.NewWriter(&buffer)
			require.NoError(t, writeOrganizationInvoiceCSV(writer, &model.OrganizationInvoiceExportContext{}, invoice))
			writer.Flush()
			require.NoError(t, writer.Error())
			out := buffer.String()
			assert.NotContains(t, out, "未独立核验")
			if tc.refund == "" {
				assert.NotContains(t, out, "其中退款")
			} else {
				assert.Contains(t, out, "其中退款（已含在当期流入中）,80.000000,,,")
			}
			if tc.wallet == "" {
				assert.NotContains(t, out, "# 钱包收支待核对")
			} else {
				assert.Contains(t, out, "# 钱包收支待核对")
				assert.Contains(t, out, "alice,,钱包收支依据不足")
				assert.Contains(t, out, "钱包问题不改变当前已确认消费金额")
			}
			if tc.problem {
				assert.Contains(t, out, "# 费用待确认")
				assert.Contains(t, out, "金额说明,已确认消费小计，仍有费用待核对")
				assert.Less(t, strings.Index(out, "金额说明"), strings.Index(out, "# 模型归类结算汇总"))
			} else {
				assert.NotContains(t, out, "# 费用待确认")
				assert.NotContains(t, out, "账户ID,任务")
			}
		})
	}
}

func TestWriteOrganizationInvoiceCSVRefundBreakdownAndProblemDeduplication(t *testing.T) {
	issue := model.OrganizationInvoiceExpenseIssue{UserId: 91, TaskId: "task-2820", Reason: "final_expense_evidence_missing"}
	invoice := &model.OrganizationInvoice{
		Period: model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-30"}, Currency: "USD",
		CoveragePeriod:  model.OrganizationInvoicePeriod{StartDate: "2026-09-01", EndDate: "2026-09-15", StartTimestamp: 1788192000, EndTimestamp: 1789444800},
		ExpenseCoverage: model.OrganizationInvoiceExpenseCoverage{Status: "complete", Issues: []model.OrganizationInvoiceExpenseIssue{issue, issue}},
		WalletWarning:   "SELECT private_table password=do-not-display",
		Accounts: []model.OrganizationInvoiceAccount{
			{UserId: 91, Username: "alice", GrossAmountUSD: "20", Financials: model.OrganizationInvoiceAccountFinancials{TotalInflowAmountUSD: "180", AIWalletDeductionAmountUSD: "100", WalletRefundAmountUSD: "80", ReconciliationStatus: "unavailable"}},
			{UserId: 92, Username: "bob", GrossAmountUSD: "0", Financials: model.OrganizationInvoiceAccountFinancials{TotalInflowAmountUSD: "0", WalletRefundAmountUSD: "0.0000000000", ReconciliationStatus: "derived"}},
		}, GrossTotalAmountUSD: "20", SettledTotalAmountUSD: "18",
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writeOrganizationInvoiceCSV(writer, &model.OrganizationInvoiceExportContext{}, invoice))
	writer.Flush()
	require.NoError(t, writer.Error())
	out := buffer.String()
	assert.Contains(t, out, "实际覆盖,2026-09-01,2026-09-15 12:00:00")
	assert.Contains(t, out, "其中退款（已含在当期流入中）,80.000000,,,,")
	assert.Contains(t, out, "用户当期流入金额,180.000000,0.000000")
	assert.Contains(t, out, "alice,task-2820,最终费用尚未确认")
	assert.Equal(t, 1, strings.Count(out, "task-2820"))
	assert.Contains(t, out, "alice,,钱包信息暂不可用")
	assert.NotContains(t, out, "private_table")
	assert.NotContains(t, out, "do-not-display")
	assert.NotContains(t, out, "bob,按流水")
	assert.Less(t, strings.Index(out, "其中退款"), strings.Index(out, "# AI 模型消费汇总"))
}
