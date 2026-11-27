package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestOperationsDeliveryPagination(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(t.TempDir() + "/operations.db")
			case "mysql":
				dsn := os.Getenv("OPS_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("OPS_TEST_MYSQL_DSN not set")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("OPS_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("OPS_TEST_POSTGRES_DSN not set")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
			require.NoError(t, db.AutoMigrate(&model.OperationsDelivery{}, &model.Option{}))

			var version string
			require.NoError(t, db.Raw(map[string]string{
				"sqlite": "SELECT sqlite_version()", "mysql": "SELECT VERSION()", "postgres": "SHOW server_version",
			}[dialect]).Scan(&version).Error)
			t.Logf("%s %s", dialect, version)

			tx := db.Begin()
			require.NoError(t, tx.Error)
			previous := model.DB
			model.DB = tx
			t.Cleanup(func() {
				model.DB = previous
				assert.NoError(t, tx.Rollback().Error)
			})
			router := gin.New()
			router.GET("/operations", GetOperationsNotifications)
			type responseData struct {
				Success bool `json:"success"`
				Data    struct {
					Total      int                        `json:"total"`
					Page       int                        `json:"page"`
					PageSize   int                        `json:"page_size"`
					Items      []model.OperationsDelivery `json:"items"`
					Deliveries []model.OperationsDelivery `json:"deliveries"`
					Settings   *model.OperationsSettings  `json:"settings"`
				} `json:"data"`
			}
			get := func(query string) responseData {
				t.Helper()
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/operations"+query, nil))
				require.Equal(t, http.StatusOK, recorder.Code)
				var response responseData
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				require.True(t, response.Success, recorder.Body.String())
				return response
			}
			assert.Empty(t, get("?scope=deliveries&p=1&page_size=2").Data.Items)
			require.NoError(t, tx.Create(&[]model.OperationsDelivery{
				{Key: "a", CreatedAt: 3, Body: "<p>First message</p>"},
				{Key: "b", CreatedAt: 2},
				{Key: "c", CreatedAt: 2},
			}).Error)

			first := get("?scope=deliveries&p=1&page_size=2").Data
			assert.Equal(t, 3, first.Total)
			assert.Equal(t, 2, first.PageSize)
			assert.Nil(t, first.Settings)
			require.Len(t, first.Items, 2)
			assert.Equal(t, "a", first.Items[0].Key)
			assert.Equal(t, "<p>First message</p>", first.Items[0].Body)
			assert.Equal(t, "c", first.Items[1].Key)
			last := get("?scope=deliveries&p=2&page_size=2").Data
			assert.Equal(t, 3, last.Total)
			require.Len(t, last.Items, 1)
			assert.Equal(t, "b", last.Items[0].Key)
			outOfRange := get("?scope=deliveries&p=9223372036854775807&page_size=100").Data
			assert.Equal(t, 3, outOfRange.Total)
			assert.NotNil(t, outOfRange.Items)
			assert.Empty(t, outOfRange.Items)
			bounded := get("?scope=deliveries&p=-1&page_size=-10").Data
			assert.Equal(t, 1, bounded.Page)
			assert.Equal(t, 1, bounded.PageSize)
			require.Len(t, bounded.Items, 1)

			settings := get("?scope=settings").Data
			assert.NotNil(t, settings.Settings)
			assert.Nil(t, settings.Deliveries)
			assert.Len(t, get("").Data.Deliveries, 3)
		})
	}
}
