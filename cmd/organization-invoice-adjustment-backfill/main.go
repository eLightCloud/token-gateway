package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/shopspring/decimal"
)

func main() {
	organizationId := flag.Int("organization-id", 0, "organization ID")
	startDate := flag.String("start-date", "", "billing start date in YYYY-MM-DD")
	endDate := flag.String("end-date", "", "billing end date in YYYY-MM-DD")
	verifiedBy := flag.Int("verified-by", 0, "verifying administrator user ID")
	apply := flag.Bool("apply", false, "persist the reviewed candidates")
	backfillTopUpQuota := flag.Bool("backfill-topup-credited-quota", false, "persist reviewed legacy successful top-up quota credits")
	legacyQuotaPerUnit := flag.Float64("legacy-quota-per-unit", 0, "reviewed quota-per-USD used by legacy top-ups in this period")
	flag.Parse()

	if err := run(*organizationId, *startDate, *endDate, *verifiedBy, *apply, *backfillTopUpQuota, *legacyQuotaPerUnit); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(organizationId int, startDate, endDate string, verifiedBy int, apply bool, backfillTopUpQuota bool, legacyQuotaPerUnit float64) error {
	if organizationId <= 0 || startDate == "" || endDate == "" {
		return errors.New("organization-id, start-date and end-date are required")
	}
	if apply && verifiedBy <= 0 {
		return errors.New("verified-by is required with --apply")
	}
	if backfillTopUpQuota && !apply {
		return errors.New("backfill-topup-credited-quota requires --apply")
	}
	if backfillTopUpQuota && legacyQuotaPerUnit <= 0 {
		return errors.New("backfill-topup-credited-quota requires --legacy-quota-per-unit")
	}

	common.InitEnv()
	common.IsMasterNode = false
	if err := model.InitDB(); err != nil {
		return err
	}
	if apply {
		if err := model.EnsureTopUpCreditedQuotaColumn(); err != nil {
			return err
		}
		if err := model.EnsureUserQuotaAdjustmentLegacyFactTable(); err != nil {
			return err
		}
		if err := model.EnsureOrganizationInvoiceSummaryTables(); err != nil {
			return err
		}
	}
	model.InitOptionMap()
	if err := model.InitLogDB(); err != nil {
		return err
	}
	defer model.CloseDB()

	period, err := model.NewOrganizationInvoicePeriod(startDate, endDate, time.Now())
	if err != nil {
		return err
	}
	candidates, err := model.PreviewLegacyUserQuotaAdjustments(organizationId, period)
	if err != nil {
		return err
	}
	encoded, err := common.Marshal(candidates)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	totalUSD := decimal.Zero
	for _, candidate := range candidates {
		amount, err := decimal.NewFromString(candidate.AmountUSD)
		if err != nil {
			return err
		}
		totalUSD = totalUSD.Add(amount)
	}
	fmt.Printf("summary: %d candidates, total_usd=%s\n", len(candidates), totalUSD.StringFixed(6))
	if !apply {
		fmt.Println("preview only")
		return nil
	}

	applied, err := model.ApplyLegacyUserQuotaAdjustmentsForOrganization(
		organizationId,
		period,
		verifiedBy,
		time.Now().Unix(),
	)
	if err != nil {
		return err
	}
	fmt.Printf("applied: %d\n", applied)
	if backfillTopUpQuota {
		updated, err := model.BackfillOrganizationInvoiceTopUpCreditedQuotas(organizationId, period, legacyQuotaPerUnit)
		if err != nil {
			return err
		}
		fmt.Printf("backfilled top-up credited quota: %d\n", updated)
	}
	return nil
}
