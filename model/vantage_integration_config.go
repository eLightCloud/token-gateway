package model

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The sensitive suffix also excludes this entire record from GET /api/option/.
const VantageIntegrationOptionKey = "VantageIntegrationSecret"

var ErrVantageIntegrationOptionProtected = errors.New("Use the Vantage integration settings endpoint.")

type VantageIntegrationConfigError struct{ Message string }

func (e *VantageIntegrationConfigError) Error() string { return e.Message }

// One existing options row owns the complete policy. Requests read this row so
// changes apply on every instance without restart, stale caches or polling.
type VantageIntegrationConfig struct {
	Enabled      bool     `json:"enabled"`
	AllowedIPv4  []string `json:"allowed_ipv4"`
	SecretDigest string   `json:"secret_digest"`
}

type VantageIntegrationSettings struct {
	Enabled          bool     `json:"enabled"`
	AllowedIPv4      []string `json:"allowed_ipv4"`
	SecretConfigured bool     `json:"secret_configured"`
}

func (config *VantageIntegrationConfig) Settings() VantageIntegrationSettings {
	return VantageIntegrationSettings{
		Enabled: config.Enabled, AllowedIPv4: slices.Clone(config.AllowedIPv4),
		SecretConfigured: config.SecretDigest != "",
	}
}

func (config *VantageIntegrationConfig) SecretMatches(presented string) bool {
	if config.SecretDigest == "" {
		return false
	}
	digest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(config.SecretDigest)) == 1
}

func (config *VantageIntegrationConfig) SourceAllowed(ip netip.Addr) bool {
	return ip.Is4() && slices.Contains(config.AllowedIPv4, ip.String())
}

// Normalize validates both saved policies and writes. Disabled policies can be
// prepared without a key/list; enabling always requires both protections.
func (config *VantageIntegrationConfig) Normalize() error {
	addresses := make([]string, 0, len(config.AllowedIPv4))
	for _, raw := range config.AllowedIPv4 {
		ip, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil || !ip.Is4() {
			return &VantageIntegrationConfigError{"Only complete IPv4 addresses are allowed."}
		}
		if !slices.Contains(addresses, ip.String()) {
			addresses = append(addresses, ip.String())
		}
	}
	config.AllowedIPv4 = addresses
	if config.SecretDigest != "" {
		digest, err := hex.DecodeString(config.SecretDigest)
		if err != nil || len(digest) != sha256.Size {
			return &VantageIntegrationConfigError{"The stored integration secret is invalid."}
		}
	}
	if config.Enabled && (config.SecretDigest == "" || len(config.AllowedIPv4) == 0) {
		return &VantageIntegrationConfigError{"An application secret and IPv4 allowlist are required to enable integration."}
	}
	return nil
}

func GetVantageIntegrationConfig() (*VantageIntegrationConfig, error) {
	var option Option
	err := DB.Where(&Option{Key: VantageIntegrationOptionKey}).First(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &VantageIntegrationConfig{AllowedIPv4: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var config VantageIntegrationConfig
	if err := common.UnmarshalJsonStr(option.Value, &config); err != nil {
		return nil, &VantageIntegrationConfigError{"The stored integration settings are invalid."}
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return &config, nil
}

// Save preserves the current secret when blank. The row lock prevents another
// instance's key rotation from being overwritten by a concurrent allowlist edit.
func SaveVantageIntegrationSettings(enabled bool, allowedIPv4 []string, secret string) (*VantageIntegrationSettings, error) {
	if secret != "" && len(secret) < 32 {
		return nil, &VantageIntegrationConfigError{"The application secret must contain at least 32 bytes."}
	}
	var settings VantageIntegrationSettings
	var err error
	for attempt := range 2 {
		err = DB.Transaction(func(tx *gorm.DB) error {
			initial, marshalErr := common.Marshal(VantageIntegrationConfig{AllowedIPv4: []string{}})
			if marshalErr != nil {
				return marshalErr
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{
				Key: VantageIntegrationOptionKey, Value: string(initial),
			}).Error; err != nil {
				return err
			}
			var option Option
			if err := lockForUpdate(tx).Where(&Option{Key: VantageIntegrationOptionKey}).First(&option).Error; err != nil {
				return err
			}
			var config VantageIntegrationConfig
			if err := common.UnmarshalJsonStr(option.Value, &config); err != nil && secret == "" {
				return &VantageIntegrationConfigError{"The stored integration settings are invalid."}
			}
			config.Enabled, config.AllowedIPv4 = enabled, allowedIPv4
			if secret != "" {
				digest := sha256.Sum256([]byte(secret))
				config.SecretDigest = hex.EncodeToString(digest[:])
			}
			if err := config.Normalize(); err != nil {
				return err
			}
			value, err := common.Marshal(config)
			if err != nil {
				return err
			}
			if err := tx.Model(&Option{}).Where(&Option{Key: VantageIntegrationOptionKey}).Update("value", string(value)).Error; err != nil {
				return err
			}
			settings = config.Settings()
			return nil
		})
		if err == nil {
			return &settings, nil
		}
		if attempt != 0 || !isVantageRetryableWriteError(err) {
			break
		}
	}
	return nil, err
}
