package model

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsureTopUpCreditedQuotaColumnMigratesPopulatedSQLiteTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE top_ups (id integer primary key, trade_no text)").Error)
	require.NoError(t, db.Exec("INSERT INTO top_ups (id, trade_no) VALUES (?, ?)", 1, "legacy-order").Error)

	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })
	require.NoError(t, EnsureTopUpCreditedQuotaColumn())

	var topUp TopUp
	require.NoError(t, db.Select("id", "credited_quota").First(&topUp, 1).Error)
	assert.Zero(t, topUp.CreditedQuota)
}
