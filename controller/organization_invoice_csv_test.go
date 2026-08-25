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
	require.Len(t, records, 17)
	assert.Equal(t, []string{"组织名称", "中关村学院"}, records[0])
	assert.Equal(t, []string{"账期", "2026-07-01 ~ 2026-07-31"}, records[1])
	assert.Equal(t, []string{"币种", "USD"}, records[2])
	assert.Equal(t, []string{"真实姓名", "张宇", "", "", "", ""}, records[4])
	assert.Equal(t, []string{"模型类别", "alice", "bob", "折前合计", "结算系数", "结算后金额"}, records[5])
	assert.Equal(t, []string{"用户上期余额", "4.000000", "2.500000", "", "", ""}, records[8])
	assert.Equal(t, []string{"用户当期充值金额", "12.500000", "3.000000", "", "", ""}, records[9])
	assert.Equal(t, []string{"用户当期消费金额", "1.250000", "0.750000", "", "", ""}, records[10])
	assert.Equal(t, []string{"用户当前余额", "6.250000", "1.500000", "", "", ""}, records[11])
	assert.Equal(t, []string{"真实姓名", "张宇", "", "", ""}, records[13])
	assert.Equal(t, []string{"模型", "alice", "bob", "合计", "占比"}, records[14])

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
