package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SeedanceEnhanceSettings struct {
	Version              uint   `json:"version"`
	Enabled              bool   `json:"enabled"`
	QueueID              string `json:"queue_id"`
	Region               string `json:"region"`
	Endpoint             string `json:"endpoint"`
	Bucket               string `json:"bucket"`
	Prefix               string `json:"prefix"`
	EnhancedPrefix       string `json:"enhanced_prefix,omitempty"`
	StoreGeneratedVideos bool   `json:"store_generated_videos"`
	MediaKitKey          string `json:"mediakit_key,omitempty"`
	AccessKeyID          string `json:"access_key_id,omitempty"`
	AccessKeySecret      string `json:"access_key_secret,omitempty"`
	MediaKitConfigured   bool   `json:"mediakit_configured"`
	OSSConfigured        bool   `json:"oss_configured"`
	EncryptionConfigured bool   `json:"encryption_configured"`
}

func (s SeedanceEnhanceSettings) Public() SeedanceEnhanceSettings {
	s.EnhancedPrefix = s.EnhancedObjectPrefix()
	s.MediaKitConfigured = s.MediaKitKey != ""
	s.OSSConfigured = s.AccessKeyID != "" && s.AccessKeySecret != "" && s.Bucket != "" && s.Region != ""
	_, encryptionErr := seedanceConfigCipher()
	s.EncryptionConfigured = encryptionErr == nil
	s.MediaKitKey, s.AccessKeyID, s.AccessKeySecret = "", "", ""
	return s
}

// Config versions written before separate prefixes used Prefix for both files.
// Preserve that directory when resolving an already completed or in-flight task.
func (s SeedanceEnhanceSettings) EnhancedObjectPrefix() string {
	if s.EnhancedPrefix != "" {
		return s.EnhancedPrefix
	}
	return s.Prefix
}

func (s SeedanceEnhanceSettings) Ready() bool {
	p := s.Public()
	return s.Enabled && p.MediaKitConfigured && p.OSSConfigured && p.EncryptionConfigured
}

func seedanceConfigCipher() (cipher.AEAD, error) {
	secret := os.Getenv("CRYPTO_SECRET")
	if secret == "" {
		secret = os.Getenv("SESSION_SECRET")
	}
	if len(secret) < 32 {
		return nil, errors.New("a stable CRYPTO_SECRET or SESSION_SECRET of at least 32 bytes is required for video credentials")
	}
	key := sha256.Sum256([]byte("seedance-enhance-v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func LoadSeedanceEnhanceSettings(version uint) (SeedanceEnhanceSettings, error) {
	var row SeedanceEnhanceConfig
	query := DB.Order("id DESC")
	if version != 0 {
		query = DB.Where("id = ?", version)
	}
	err := query.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && version == 0 {
		return SeedanceEnhanceSettings{Prefix: "sd", EnhancedPrefix: "sd-enhance"}, nil
	}
	if err != nil {
		return SeedanceEnhanceSettings{}, err
	}
	aead, err := seedanceConfigCipher()
	if err != nil {
		return SeedanceEnhanceSettings{}, err
	}
	data, err := base64.StdEncoding.DecodeString(row.Ciphertext)
	if err != nil || len(data) < aead.NonceSize() {
		return SeedanceEnhanceSettings{}, errors.New("invalid encrypted video configuration")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("seedance-enhance-config-v1"))
	if err != nil {
		return SeedanceEnhanceSettings{}, errors.New("cannot decrypt video configuration; verify the stable server encryption key")
	}
	var config SeedanceEnhanceSettings
	if err = common.Unmarshal(plain, &config); err != nil {
		return config, err
	}
	config.Version = row.ID
	return config, nil
}

var seedanceBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var seedanceRegionPattern = regexp.MustCompile(`^cn-[a-z0-9-]+$`)

func SaveSeedanceEnhanceSettings(update SeedanceEnhanceSettings) (SeedanceEnhanceSettings, error) {
	aead, err := seedanceConfigCipher()
	if err != nil {
		return update, err
	}
	current, err := LoadSeedanceEnhanceSettings(0)
	if err != nil {
		return update, err
	}
	var saved SeedanceEnhanceSettings
	err = DB.Transaction(func(tx *gorm.DB) error {
		// Config versions are immutable. Optimistic version comparison is serialized
		// by the existing options row; credentials are never stored in OptionMap.
		const optionKey = "SeedanceEnhanceConfigVersionSecret"
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: optionKey, Value: ""}).Error; err != nil {
			return err
		}
		var lock Option
		if err := lockForUpdate(tx).Where(&Option{Key: optionKey}).First(&lock).Error; err != nil {
			return err
		}
		var previous SeedanceEnhanceConfig
		result := tx.Order("id DESC").First(&previous)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if previous.ID != update.Version {
			return errors.New("video settings changed; reload before saving")
		}
		if update.MediaKitKey == "" {
			update.MediaKitKey = current.MediaKitKey
		}
		if update.AccessKeyID == "" {
			update.AccessKeyID = current.AccessKeyID
		}
		if update.AccessKeySecret == "" {
			update.AccessKeySecret = current.AccessKeySecret
		}
		for _, secret := range []string{update.MediaKitKey, update.AccessKeyID, update.AccessKeySecret} {
			if len(secret) > 4096 || strings.ContainsAny(secret, "\r\n\x00") {
				return errors.New("invalid video credential")
			}
		}
		if update.Region != "" && !seedanceRegionPattern.MatchString(update.Region) {
			return errors.New("invalid OSS region")
		}
		if update.Bucket != "" && !seedanceBucketPattern.MatchString(update.Bucket) {
			return errors.New("invalid OSS bucket")
		}
		if update.Endpoint != "" {
			endpoint, err := url.Parse(update.Endpoint)
			if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.Host != "oss-"+update.Region+".aliyuncs.com" {
				return errors.New("OSS endpoint must be the region's public HTTPS endpoint")
			}
		}
		update.Prefix = strings.Trim(update.Prefix, "/")
		if update.Prefix == "" {
			update.Prefix = "sd"
		}
		update.EnhancedPrefix = strings.Trim(update.EnhancedPrefix, "/")
		if update.EnhancedPrefix == "" {
			update.EnhancedPrefix = update.Prefix
		}
		for _, prefix := range []string{update.Prefix, update.EnhancedPrefix} {
			if len(prefix) > 128 || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\\\r\n\x00") {
				return errors.New("invalid OSS object prefix")
			}
		}
		if len(update.QueueID) > 128 {
			return errors.New("invalid MediaKit queue id")
		}
		if update.StoreGeneratedVideos && !update.Public().OSSConfigured {
			return errors.New("OSS configuration is required for generated video storage")
		}
		if update.Enabled && !(update.MediaKitKey != "" && update.AccessKeyID != "" && update.AccessKeySecret != "" && update.Region != "" && update.Bucket != "") {
			return errors.New("MediaKit and OSS configuration are required")
		}
		plain, err := common.Marshal(update)
		if err != nil {
			return err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return err
		}
		encrypted := aead.Seal(nonce, nonce, plain, []byte("seedance-enhance-config-v1"))
		row := SeedanceEnhanceConfig{CreatedAt: time.Now().Unix(), Ciphertext: base64.StdEncoding.EncodeToString(encrypted)}
		if err = tx.Create(&row).Error; err != nil {
			return err
		}
		if err = tx.Model(&Option{}).Where(&Option{Key: optionKey}).Update("value", fmt.Sprint(row.ID)).Error; err != nil {
			return err
		}
		update.Version = row.ID
		saved = update.Public()
		return nil
	})
	return saved, err
}
