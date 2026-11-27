package controller

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_visibility"
	"github.com/gin-gonic/gin"
)

type catalogVendorRegion struct {
	Name string `json:"name"`
	model_visibility.VendorClassification
	Models []string `json:"models"`
}

func GetVendorCatalogRegions(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	pricing := filterPricingByUsableGroups(model.GetPricing(), service.GetUserUsableGroups(""))
	vendors := slices.Clone(model.GetVendors())
	slices.SortFunc(vendors, func(a, b model.PricingVendor) int {
		return strings.Compare(a.Name, b.Name)
	})

	settings := model_visibility.Current()
	rows := make([]catalogVendorRegion, 0, len(vendors))
	byName := make(map[string]int, len(vendors))
	byID := make(map[int]int, len(vendors))
	for _, vendor := range vendors {
		key := model_visibility.VendorNameKey(vendor.Name)
		if key == "" {
			continue
		}
		index, exists := byName[key]
		if !exists {
			index = len(rows)
			byName[key] = index
			rows = append(rows, catalogVendorRegion{
				Name: vendor.Name, VendorClassification: settings.ClassifyVendor(vendor.Name), Models: []string{},
			})
		}
		byID[vendor.ID] = index
	}
	for _, item := range pricing {
		if index, ok := byID[item.VendorID]; ok {
			rows[index].Models = append(rows[index].Models, item.ModelName)
		}
	}
	for i := range rows {
		slices.Sort(rows[i].Models)
	}
	common.ApiSuccess(c, rows)
}

func UpdateVendorCatalogRegion(c *gin.Context) {
	var input struct {
		Name   string                  `json:"name"`
		Region model_visibility.Region `json:"region"`
	}
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	key := model_visibility.VendorNameKey(input.Name)
	if key == "" || !slices.ContainsFunc(model.GetVendors(), func(vendor model.PricingVendor) bool {
		return model_visibility.VendorNameKey(vendor.Name) == key
	}) {
		common.ApiErrorT(c, "The vendor no longer exists. Reload the vendor list.")
		return
	}
	if err := model.SetModelCatalogVendorRegion(key, input.Region); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}
