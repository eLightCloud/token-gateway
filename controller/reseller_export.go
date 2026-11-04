package controller

import (
	"database/sql"
	"encoding/csv"
	"os"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Materialize an export before committing HTTP success. A database error on any
// page must never leave an apparently valid, truncated reconciliation file.
func resellerCSVExport(c *gin.Context, filename string, write func(*gorm.DB, *csv.Writer) error) {
	file, err := os.CreateTemp("", "reseller-export-*.csv")
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		options.Isolation = sql.LevelSerializable
	}
	err = model.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		writer := csv.NewWriter(file)
		if err := write(tx, writer); err != nil {
			return err
		}
		writer.Flush()
		return writer.Error()
	}, options)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	info, err := file.Stat()
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	if _, err = file.Seek(0, 0); err != nil {
		resellerResponse(c, nil, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.DataFromReader(200, info.Size(), "text/csv; charset=utf-8", file, nil)
}
