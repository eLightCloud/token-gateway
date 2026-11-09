package model

import (
	"bytes"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOptionReloadSkipsDedicatedVantageConfig(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&Option{}))
	previousDB, previousMap, previousPolicy := DB, common.OptionMap, CurrentRequestPolicy()
	DB, common.OptionMap = db, map[string]string{}
	t.Cleanup(func() {
		DB, common.OptionMap = previousDB, previousMap
		requestPolicySnapshot.Store(previousPolicy)
	})
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previousWriter
		common.LogWriterMu.Unlock()
	})
	secret := strings.Repeat("k", 32)
	_, err = SaveVantageIntegrationSettings(true, []string{"127.0.0.1"}, secret)
	require.NoError(t, err)
	require.NoError(t, db.Create(&[]Option{
		{Key: "OptionReloadMarker", Value: "loaded"},
		{Key: strings.ToUpper(VantageIntegrationOptionKey) + " ", Value: "private-marker"},
	}).Error)

	for range 2 {
		loadOptionsFromDatabase()
	}
	assert.NotContains(t, logs.String(), "ignored invalid option")
	assert.NotContains(t, logs.String(), secret)
	assert.NotContains(t, logs.String(), "private-marker")
	assert.Equal(t, map[string]string{"OptionReloadMarker": "loaded"}, common.OptionMap)
	config, err := GetVantageIntegrationConfig()
	require.NoError(t, err)
	assert.True(t, config.Enabled)
	assert.True(t, config.SecretMatches(secret))
	assert.False(t, config.SecretMatches("wrong"))
	for _, key := range []string{VantageIntegrationOptionKey, strings.ToUpper(VantageIntegrationOptionKey) + " "} {
		assert.ErrorIs(t, UpdateOption(key, "replacement"), ErrVantageIntegrationOptionProtected)
		assert.ErrorIs(t, UpdateOptionsBulk(map[string]string{key: "replacement"}), ErrVantageIntegrationOptionProtected)
	}
}
