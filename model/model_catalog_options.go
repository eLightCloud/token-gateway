package model

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/model_visibility"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Serializes catalog option commits and publication with database reloads on this node.
var modelVisibilityOptionMutex sync.Mutex

func SetModelCatalogVendorRegion(name string, region model_visibility.Region) error {
	key := model_visibility.VendorNameKey(name)
	if key == "" || (region != model_visibility.RegionAuto && !region.IsClassification()) {
		return common.NewMessage("Invalid parameters")
	}
	modelVisibilityOptionMutex.Lock()
	defer modelVisibilityOptionMutex.Unlock()

	var value string
	option := Option{Key: model_visibility.VendorRegionsOption, Value: "{}"}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		// Ensure the row exists and acquire the SQLite write lock before reading it.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&option).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&option, &Option{Key: option.Key}).Error; err != nil {
			return err
		}
		regions, err := model_visibility.ParseVendorRegions(option.Value)
		if err != nil {
			return err
		}
		if region == model_visibility.RegionAuto {
			delete(regions, key)
		} else {
			regions[key] = region
		}
		encoded, err := common.Marshal(regions)
		if err != nil {
			return err
		}
		value = string(encoded)
		if value == option.Value {
			return nil
		}
		return tx.Model(&option).Update("value", value).Error
	}); err != nil {
		return err
	}
	if err := updateOptionMap(option.Key, value); err != nil {
		return err
	}
	BumpSyncTopic(SyncTopicOptions)
	return nil
}
