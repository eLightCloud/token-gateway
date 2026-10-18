package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	kind := flag.String("database-type", "postgres", "sqlite, mysql or postgres")
	dsnEnv := flag.String("dsn-env", "LOG_INDEX_DSN", "environment variable containing the target log database DSN")
	candidate := flag.String("candidate", "time", "time, source-header, wallet-structured, wallet-unmarked or wallet-legacy (covering candidates require PostgreSQL 11+)")
	phase := flag.String("phase", "verify", "prepare, verify, activate, recover; covering candidates also support drop; plan/load comparison is manual")
	timeout := flag.Duration("timeout", 5*time.Minute, "maintenance execution deadline")
	flag.Parse()
	if err := run(*kind, os.Getenv(*dsnEnv), *phase, *timeout, *candidate); err != nil {
		// Driver errors can include the DSN. Keep credentials out of stdout.
		var diagnostic model.OrganizationLogIndexMaintenanceError
		if errors.As(err, &diagnostic) {
			fmt.Fprintln(os.Stderr, "log index maintenance failed:", diagnostic)
		} else {
			fmt.Fprintln(os.Stderr, "log index maintenance failed; verify connectivity, index structure and database diagnostics")
		}
		os.Exit(1)
	}
	fmt.Println("log index maintenance completed:", *candidate, *phase)
}

func run(kind, dsn, phase string, timeout time.Duration, candidate string) error {
	if dsn == "" || timeout <= 0 {
		return fmt.Errorf("a target DSN and positive timeout are required")
	}
	var driver gorm.Dialector
	switch kind {
	case "postgres":
		driver = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})
	case "mysql":
		driver = mysql.Open(dsn)
	case "sqlite":
		driver = sqlite.Open(dsn)
	default:
		return fmt.Errorf("unsupported database type")
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if candidate == "source-header" {
		return model.MaintainOrganizationLogSourceIndex(ctx, db, common.DatabaseType(kind), phase)
	}
	if candidate == "wallet-structured" || candidate == "wallet-unmarked" || candidate == "wallet-legacy" {
		return model.MaintainOrganizationLogWalletIndex(ctx, db, common.DatabaseType(kind), phase, candidate)
	}
	if candidate != "time" {
		return model.OrganizationLogIndexMaintenanceError("unknown index candidate")
	}
	return model.MaintainOrganizationLogTimeIndex(ctx, db, common.DatabaseType(kind), phase)
}
