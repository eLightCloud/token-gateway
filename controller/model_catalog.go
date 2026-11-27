package controller

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_visibility"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func GetModelCatalog(c *gin.Context) {
	pricing := model.GetPricing()
	var group string
	groupRatio := ratio_setting.GetGroupRatioCopy()
	if userID := c.GetInt("id"); userID > 0 {
		if user, err := model.GetUserCache(userID); err == nil {
			group = user.Group
			for name := range groupRatio {
				if ratio, ok := ratio_setting.GetGroupGroupRatio(group, name); ok {
					groupRatio[name] = ratio
				}
			}
		}
	}

	usableGroups := service.GetUserUsableGroups(group)
	pricing = filterPricingByUsableGroups(pricing, usableGroups)
	vendors := model.GetVendors()
	pricing, visibilityFilter, err := modelCatalogPricing(c, pricing, vendors, usableGroups)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}

	for name := range groupRatio {
		if _, usable := usableGroups[name]; !usable {
			delete(groupRatio, name)
		}
	}

	response := gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            vendors,
		"group_ratio":        groupRatio,
		"usable_group":       usableGroups,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        service.GetUserAutoGroup(group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	}
	if visibilityFilter != "" {
		response["visibility_filter"] = visibilityFilter
		response["vendors"] = catalogVendors(pricing, vendors)
	}
	c.JSON(200, response)
}

// modelCatalogPricing applies the catalog rules to the same candidates used by GetPricing.
func modelCatalogPricing(c *gin.Context, pricing []model.Pricing, vendors []model.PricingVendor, usableGroups map[string]string) ([]model.Pricing, string, error) {
	settings := model_visibility.Current()
	userID := c.GetInt("id")
	if userID <= 0 {
		if !settings.DomesticOnly {
			return pricing, "", nil
		}
		domestic := make(map[int]bool)
		for _, vendor := range vendors {
			if settings.ClassifyVendor(vendor.Name).Region == model_visibility.RegionDomestic {
				domestic[vendor.ID] = true
			}
		}
		filtered := make([]model.Pricing, 0, len(pricing))
		for _, item := range pricing {
			if domestic[item.VendorID] {
				filtered = append(filtered, item)
			}
		}
		return filtered, "domestic", nil
	}

	if !settings.DiscountedOnly || c.GetInt("role") >= common.RoleAdminUser {
		visible, err := resellerVisiblePricing(userID, pricing)
		return visible, "", err
	}
	kind, err := model.ResellerUserKind(model.DB, userID)
	if err != nil {
		return nil, "", err
	}
	if kind == "agent" {
		return pricing, "", nil
	}

	names := make([]string, 0, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
	}
	if len(names) == 0 {
		return []model.Pricing{}, "discounted", nil
	}
	snapshots, err := model.LoadPersonalDisplayPricings(userID, names)
	if err != nil {
		return nil, "", err
	}
	discounted, err := model.DiscountedCatalogModels(snapshots, usableGroups)
	if err != nil {
		return nil, "", err
	}
	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if !discounted[item.ModelName] {
			continue
		}
		if kind == "agent_customer" {
			item.BillingPluginVariants = nil
		}
		filtered = append(filtered, item)
	}
	return filtered, "discounted", nil
}

func catalogVendors(pricing []model.Pricing, vendors []model.PricingVendor) []model.PricingVendor {
	visible := make(map[int]bool)
	for _, item := range pricing {
		visible[item.VendorID] = true
	}
	return slices.DeleteFunc(slices.Clone(vendors), func(vendor model.PricingVendor) bool {
		return !visible[vendor.ID]
	})
}
