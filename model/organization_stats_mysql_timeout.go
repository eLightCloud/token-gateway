package model

import (
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A per-statement SELECT hint avoids leaking a session setting through the
// connection pool. MySQL ignores hints on statements it cannot bound; the
// driver's context remains the outer cancellation boundary.
type organizationStatsMySQLTimeout int

func (organizationStatsMySQLTimeout) Name() string { return "SELECT" }
func (limit organizationStatsMySQLTimeout) Build(builder clause.Builder) {
	builder.WriteString("/*+ MAX_EXECUTION_TIME(")
	builder.WriteString(strconv.Itoa(int(limit)))
	builder.WriteString(") */ ")
}
func (limit organizationStatsMySQLTimeout) ModifyStatement(statement *gorm.Statement) {
	selectClause := statement.Clauses["SELECT"]
	selectClause.Name = "SELECT"
	selectClause.AfterNameExpression = limit
	statement.Clauses["SELECT"] = selectClause
}
