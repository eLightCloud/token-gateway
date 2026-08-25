package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelInfoScanSupportsDatabaseJSONTypes(t *testing.T) {
	const channelInfoJSON = `{"is_multi_key":true,"multi_key_size":2,"multi_key_mode":"random"}`

	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "bytes", value: []byte(channelInfoJSON)},
		{name: "string", value: channelInfoJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var channelInfo ChannelInfo
			require.NoError(t, channelInfo.Scan(tt.value))
			assert.True(t, channelInfo.IsMultiKey)
			assert.Equal(t, 2, channelInfo.MultiKeySize)
			assert.Equal(t, "random", string(channelInfo.MultiKeyMode))
		})
	}
}

func TestChannelInfoScanRejectsInvalidJSON(t *testing.T) {
	var channelInfo ChannelInfo
	require.Error(t, channelInfo.Scan("{"))
}
