package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWalletReminderLifecycle(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := "local"
			switch dialect {
			case "mysql":
				dsn = os.Getenv("TEST_MYSQL_DSN")
			case "postgres":
				dsn = os.Getenv("TEST_POSTGRES_DSN")
			}
			if dsn == "" {
				t.Skip("test database DSN is not configured")
			}
			t.Setenv("WALLET_REMINDER_TEST_DSN", dsn)
			oldPath, oldDB, oldType := common.SQLitePath, DB, common.MainDatabaseType()
			oldEnabled, oldRate := common.QuotaRemindEnabled, common.QuotaPerUnit
			t.Cleanup(func() {
				common.SQLitePath, DB = oldPath, oldDB
				common.SetMainDatabaseType(oldType)
				common.QuotaRemindEnabled, common.QuotaPerUnit = oldEnabled, oldRate
			})
			common.SQLitePath = filepath.Join(t.TempDir(), "wallet.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_txlock=immediate"
			common.QuotaRemindEnabled, common.QuotaPerUnit = true, 500_000
			db, dbType, err := chooseDB("WALLET_REMINDER_TEST_DSN", false)
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			DB = db
			common.SetMainDatabaseType(dbType)

			require.NoError(t, db.AutoMigrate(&User{}, &TaskSettlementJournal{}))
			require.NoError(t, migrateWalletReminders(db))
			const usd int64 = 500_000
			now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
			ctx := context.Background()

			pending := func(t *testing.T, userID int, at time.Time, want int64) WalletReminder {
				t.Helper()
				due, err := ListDueWalletReminders(ctx, at, 100)
				require.NoError(t, err)
				for _, row := range due {
					if row.UserID == userID {
						require.Equal(t, want*usd, row.PendingThreshold)
						return row
					}
				}
				require.Zero(t, want, "missing scheduled reminder")
				return WalletReminder{}
			}
			claim := func(t *testing.T, row WalletReminder, at time.Time) {
				t.Helper()
				claimed, err := ClaimWalletReminder(ctx, row, at)
				require.NoError(t, err)
				require.True(t, claimed)
			}
			user := func(t *testing.T, mode string) int {
				t.Helper()
				account := User{Username: fmt.Sprintf("wr%x", time.Now().UnixNano()), Status: common.UserStatusEnabled}
				account.SetSetting(dto.UserSetting{QuotaWarningMode: mode})
				require.NoError(t, db.Create(&account).Error)
				t.Cleanup(func() {
					require.NoError(t, db.Delete(&WalletReminder{}, account.Id).Error)
					require.NoError(t, db.Unscoped().Delete(&User{}, account.Id).Error)
				})
				return account.Id
			}
			credit := func(t *testing.T, userID int, amount int64) {
				t.Helper()
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return creditTopUpQuota(tx, userID, amount*usd, nil)
				}))
			}
			spend := func(t *testing.T, userID int, before, amount int64, at time.Time) {
				t.Helper()
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return adjustUserWalletInTx(tx, userID, -amount*usd)
				}))
				require.NoError(t, ObserveWalletSpend(userID, before*usd, amount*usd, at))
			}

			var version string
			versionSQL := "select version()"
			if dbType == common.DatabaseTypeSQLite {
				versionSQL = "select sqlite_version()"
			}
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database version %s", version)

			t.Run("all four standard tiers", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 105)
				before := int64(105)
				for i, threshold := range []int64{100, 30, 10, 2} {
					at := now.Add(time.Duration(i) * 11 * time.Minute)
					after := threshold - 1
					spend(t, id, before, before-after, at)
					claim(t, pending(t, id, at, threshold), at)
					before = after
				}
			})

			t.Run("twenty dollars respects strict thresholds and cooldown", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 20)
				pending(t, id, now, 0)
				spend(t, id, 20, 10, now)
				pending(t, id, now, 0)
				spend(t, id, 10, 1, now)
				claim(t, pending(t, id, now, 10), now)
				spend(t, id, 9, 8, now.Add(time.Minute))
				pending(t, id, now.Add(9*time.Minute), 0)
				claim(t, pending(t, id, now.Add(10*time.Minute), 2), now.Add(10*time.Minute))
				spend(t, id, 1, 1, now.Add(21*time.Minute))
				pending(t, id, now.Add(time.Hour), 0)
			})

			t.Run("single charge and concurrent claims send only the urgent tier", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 20)
				spend(t, id, 20, 19, now)
				row := pending(t, id, now, 2)
				spend(t, id, 1, 1, now)
				type result struct {
					claimed bool
					err     error
				}
				start, done := make(chan struct{}), make(chan result, 4)
				for range 4 {
					go func() {
						<-start
						err := ObserveWalletSpend(id, 20*usd, 19*usd, now)
						if err != nil {
							done <- result{err: err}
							return
						}
						claimed, err := ClaimWalletReminder(ctx, row, now)
						done <- result{claimed: claimed, err: err}
					}()
				}
				close(start)
				count := 0
				for range 4 {
					res := <-done
					require.NoError(t, res.err)
					if res.claimed {
						count++
					}
				}
				assert.Equal(t, 1, count)
				pending(t, id, now.Add(time.Hour), 0)
			})

			t.Run("simultaneous spends cross a tier even when each snapshot would miss it", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 20)
				start, done := make(chan struct{}), make(chan error, 2)
				for range 2 {
					go func() {
						<-start
						err := db.Transaction(func(tx *gorm.DB) error {
							return adjustUserWalletInTx(tx, id, -6*usd)
						})
						if err == nil {
							err = ObserveWalletSpend(id, 20*usd, 6*usd, now)
						}
						done <- err
					}()
				}
				close(start)
				for range 2 {
					require.NoError(t, <-done)
				}
				claim(t, pending(t, id, now, 10), now)
				pending(t, id, now.Add(time.Hour), 0)
			})

			t.Run("recharge restores only reached tiers and refund does not rearm", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 20)
				spend(t, id, 20, 19, now)
				claim(t, pending(t, id, now, 2), now)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return adjustUserWalletInTx(tx, id, 2*usd)
				}))
				later := now.Add(11 * time.Minute)
				spend(t, id, 3, 2, later)
				pending(t, id, later, 0)
				credit(t, id, 4)
				spend(t, id, 5, 4, later)
				old := pending(t, id, later, 2)
				credit(t, id, 19)
				claimed, err := ClaimWalletReminder(ctx, old, later)
				require.NoError(t, err)
				assert.False(t, claimed)
				pending(t, id, later, 0)
				spend(t, id, 20, 11, later)
				claim(t, pending(t, id, later, 10), later)
			})

			t.Run("step mode and cooldown keep the lowest queued tier", func(t *testing.T) {
				id := user(t, dto.QuotaWarningModeStep)
				credit(t, id, 60)
				spend(t, id, 60, 15, now)
				claim(t, pending(t, id, now, 50), now)
				spend(t, id, 45, 6, now.Add(time.Minute))
				spend(t, id, 39, 24, now.Add(2*time.Minute))
				claim(t, pending(t, id, now.Add(10*time.Minute), 20), now.Add(10*time.Minute))
			})

			t.Run("durable settlement records a crossing once", func(t *testing.T) {
				id := user(t, "")
				credit(t, id, 20)
				charge := 11 * int(usd)
				requestID := fmt.Sprintf("wallet-reminder:%d", id)
				_, err := EnsureTaskSettlementJournal(&TaskSettlementJournal{
					RequestId: requestID, UserId: id, Operation: TaskSettlementOperationSettle,
					BillingSource: "wallet", FinalQuota: charge, Delta: charge, LogDone: true,
				})
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, db.Where("request_id = ?", requestID).Delete(&TaskSettlementJournal{}).Error)
				})
				_, applied, err := ApplyTaskSettlementJournal(requestID)
				require.NoError(t, err)
				require.True(t, applied)
				at := time.Now()
				claim(t, pending(t, id, at, 10), at)
				_, applied, err = ApplyTaskSettlementJournal(requestID)
				require.NoError(t, err)
				assert.False(t, applied)
				pending(t, id, at.Add(time.Hour), 0)
				var account User
				require.NoError(t, db.First(&account, id).Error)
				assert.EqualValues(t, 9*usd, account.Quota)
			})

			t.Run("legacy batch observes a crossing only after wallet flush", func(t *testing.T) {
				previousBatchMode := common.BatchUpdateEnabled
				common.BatchUpdateEnabled = true
				t.Cleanup(func() { common.BatchUpdateEnabled = previousBatchMode })
				id := user(t, "")
				credit(t, id, 20)
				require.NoError(t, DecreaseUserQuota(id, 11*usd, false))
				ObserveLegacyWalletSettlement(id, 20*usd, 11*usd)
				pending(t, id, time.Now(), 0)
				var account User
				require.NoError(t, db.First(&account, id).Error)
				assert.EqualValues(t, 20*usd, account.Quota)

				batchUpdate()
				require.NoError(t, db.First(&account, id).Error)
				assert.EqualValues(t, 9*usd, account.Quota)
				at := time.Now()
				claim(t, pending(t, id, at, 10), at)
				batchUpdate()
				pending(t, id, at.Add(time.Hour), 0)
			})

			t.Run("existing balance is not alerted on migration", func(t *testing.T) {
				id := user(t, "")
				require.NoError(t, db.Model(&User{}).Where("id = ?", id).Update("quota", 20*usd).Error)
				require.NoError(t, migrateWalletReminders(db))
				require.True(t, db.Migrator().HasIndex(&WalletReminder{}, "DeliverAfter"))
				pending(t, id, now, 0)
				spend(t, id, 20, 11, now)
				row := pending(t, id, now, 10)
				require.Error(t, db.Create(&row).Error)
				claim(t, row, now)
			})
		})
	}
}
