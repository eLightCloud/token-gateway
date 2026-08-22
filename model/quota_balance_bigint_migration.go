package model

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

type quotaBalanceColumn struct {
	table  string
	column string
}

var quotaBalanceColumns = []quotaBalanceColumn{
	{table: "users", column: "quota"},
	{table: "users", column: "used_quota"},
	{table: "users", column: "aff_quota"},
	{table: "users", column: "aff_history"},
	{table: "tokens", column: "remain_quota"},
	{table: "tokens", column: "used_quota"},
	{table: "redemptions", column: "quota"},
	{table: "user_quota_adjustments", column: "delta_quota"},
	{table: "user_quota_adjustments", column: "balance_before"},
	{table: "user_quota_adjustments", column: "balance_after"},
}

type mysqlQuotaColumnMetadata struct {
	DataType      string  `gorm:"column:DATA_TYPE"`
	IsNullable    string  `gorm:"column:IS_NULLABLE"`
	ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
}

func migrateQuotaBalanceColumns() error {
	for _, target := range quotaBalanceColumns {
		if !DB.Migrator().HasTable(target.table) || !DB.Migrator().HasColumn(target.table, target.column) {
			continue
		}
		if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
			continue
		}

		wide, err := quotaBalanceColumnIsWide(target)
		if err != nil {
			return err
		}
		if wide {
			continue
		}

		switch {
		case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
			statement := fmt.Sprintf(
				`ALTER TABLE "%s" ALTER COLUMN "%s" TYPE BIGINT USING "%s"::BIGINT`,
				target.table,
				target.column,
				target.column,
			)
			if err := DB.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrate %s.%s to bigint: %w", target.table, target.column, err)
			}
		case common.UsingMainDatabase(common.DatabaseTypeMySQL):
			metadata, err := mysqlQuotaColumn(target)
			if err != nil {
				return err
			}
			nullability := " NULL"
			if strings.EqualFold(metadata.IsNullable, "NO") {
				nullability = " NOT NULL"
			}
			defaultClause := ""
			if metadata.ColumnDefault != nil {
				defaultValue, err := strconv.ParseInt(*metadata.ColumnDefault, 10, 64)
				if err != nil {
					return fmt.Errorf("unsupported default for %s.%s: %q", target.table, target.column, *metadata.ColumnDefault)
				}
				defaultClause = fmt.Sprintf(" DEFAULT %d", defaultValue)
			}
			statement := fmt.Sprintf(
				"ALTER TABLE `%s` MODIFY COLUMN `%s` BIGINT%s%s",
				target.table,
				target.column,
				nullability,
				defaultClause,
			)
			if err := DB.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrate %s.%s to bigint: %w", target.table, target.column, err)
			}
		default:
			return fmt.Errorf("quota bigint migration does not support database %q", common.MainDatabaseType())
		}
	}
	return nil
}

func quotaBalanceColumnIsWide(target quotaBalanceColumn) (bool, error) {
	switch {
	case common.UsingMainDatabase(common.DatabaseTypePostgreSQL):
		var dataType string
		err := DB.Raw(
			`SELECT data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			target.table,
			target.column,
		).Scan(&dataType).Error
		if err != nil {
			return false, fmt.Errorf("inspect %s.%s: %w", target.table, target.column, err)
		}
		return strings.EqualFold(dataType, "bigint"), nil
	case common.UsingMainDatabase(common.DatabaseTypeMySQL):
		metadata, err := mysqlQuotaColumn(target)
		if err != nil {
			return false, err
		}
		return strings.EqualFold(metadata.DataType, "bigint"), nil
	case common.UsingMainDatabase(common.DatabaseTypeSQLite):
		return true, nil
	default:
		return false, fmt.Errorf("quota bigint migration does not support database %q", common.MainDatabaseType())
	}
}

func mysqlQuotaColumn(target quotaBalanceColumn) (*mysqlQuotaColumnMetadata, error) {
	var metadata mysqlQuotaColumnMetadata
	err := DB.Raw(
		`SELECT DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		target.table,
		target.column,
	).Scan(&metadata).Error
	if err != nil {
		return nil, fmt.Errorf("inspect %s.%s: %w", target.table, target.column, err)
	}
	if metadata.DataType == "" {
		return nil, fmt.Errorf("missing metadata for %s.%s", target.table, target.column)
	}
	return &metadata, nil
}

func verifyQuotaBalanceSchema() error {
	for _, target := range quotaBalanceColumns {
		if !DB.Migrator().HasTable(target.table) || !DB.Migrator().HasColumn(target.table, target.column) {
			return fmt.Errorf("quota balance schema is missing %s.%s", target.table, target.column)
		}
		wide, err := quotaBalanceColumnIsWide(target)
		if err != nil {
			return err
		}
		if !wide {
			return fmt.Errorf("quota balance schema is not 64-bit: %s.%s", target.table, target.column)
		}
	}
	return nil
}
