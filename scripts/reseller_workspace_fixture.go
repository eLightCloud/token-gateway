//go:build ignore

// Run only on loopback for browser QA. Authentication is a synthetic fixture;
// business endpoints execute the production controller/model implementation.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if os.Getenv("RESELLER_BROWSER_FIXTURE") != "synthetic-only" {
		log.Fatal("set RESELLER_BROWSER_FIXTURE=synthetic-only to start isolated browser QA")
	}
	directory, err := os.MkdirTemp("", "reseller-browser-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(directory)
	db, err := gorm.Open(sqlite.Open(filepath.Join(directory, "fixture.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal(err)
	}
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.SMTPServer, common.SMTPPort = "127.0.0.1", 1
	common.QuotaPerUnit = 500000
	tables := []any{&model.RealtimePricingHead{}, &model.RealtimePricingRule{}, &model.RealtimePricingHistory{}, &model.RealtimePricingState{}, &model.Organization{}, &model.OrganizationMember{}, &model.OrganizationDiscountSnapshot{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.AuthFlow{}, &model.AuditLog{}, &model.User{}, &model.Channel{}, &model.Ability{}, &model.ResellerIdentity{}, &model.ResellerAccount{}, &model.ResellerQuotaAdjustment{}, &model.ResellerConfigHead{}, &model.ResellerModelConfig{}, &model.ResellerConfigRevision{}, &model.ResellerFundingSnapshot{}, &model.ResellerRequest{}, &model.ResellerEvent{}, &model.ResellerBillingReceipt{}, &model.ResellerProjection{}, &model.TaskSettlementJournal{}}
	if err := db.AutoMigrate(tables...); err != nil {
		log.Fatal(err)
	}
	users := []model.User{{Id: 1, Username: "platform-admin", DisplayName: "Platform administrator", Email: "admin@example.invalid", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "qa-root"}, {Id: 2, Username: "demo-reseller", DisplayName: "Demo reseller", Email: "agent@example.invalid", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", ResellerKind: "agent", AffCode: "qa-agent"}, {Id: 3, Username: "demo-customer", DisplayName: "Demo customer", Email: "customer@example.invalid", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", ResellerKind: "agent_customer", Quota: 5000000, AffCode: "qa-customer"}, {Id: 4, Username: "ordinary-user", Email: "ordinary@example.invalid", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 1000000, AffCode: "qa-ordinary"}}
	if err := db.Create(&users).Error; err != nil {
		log.Fatal(err)
	}
	for _, identity := range []model.ResellerIdentity{{UserID: 2, Kind: "agent", Activation: "active"}, {UserID: 3, Kind: "agent_customer", AgentUserID: 2, Activation: "active"}} {
		if err := db.Create(&identity).Error; err != nil {
			log.Fatal(err)
		}
	}
	if err := db.Create(&model.ResellerAccount{UserID: 2, Enabled: true, Balance: 50000000, PublishedAt: common.GetTimestamp()}).Error; err != nil {
		log.Fatal(err)
	}
	channel := model.Channel{Id: 11, Name: "Synthetic channel", Models: "demo-model", Status: common.ChannelStatusEnabled, Type: 1, Key: "synthetic-never-used", Group: "default"}
	if err := db.Create(&channel).Error; err != nil {
		log.Fatal(err)
	}
	if err := db.Create(&model.Ability{ChannelId: 11, Model: "demo-model", Group: "default", Enabled: true}).Error; err != nil {
		log.Fatal(err)
	}
	input := model.ResellerConfigInput{Items: []model.ResellerConfigItem{{Model: "demo-model", ChannelID: 11, Authorized: true, Ratio: "0.8"}}}
	if _, err := model.SaveResellerConfig(1, 2, true, input); err != nil {
		log.Fatal(err)
	}
	input.Items[0].Ratio = "0.9"
	if _, err := model.SaveResellerConfig(2, 3, false, input); err != nil {
		log.Fatal(err)
	}
	if os.Getenv("RESELLER_REALTIME_FIXTURE") == "active" {
		if _, err := model.MigrateRealtimePricing(1, "freeze"); err != nil {
			log.Fatal(err)
		}
		if conflicts, err := model.MigrateRealtimePricing(1, "activate"); err != nil || len(conflicts) > 0 {
			log.Fatalf("fixture migration: %v %v", err, conflicts)
		}
	}
	for i := range 3 {
		fact := types.PersonalBillingFact{RequestID: fmt.Sprintf("synthetic-request-%d", i), OperationID: model.PersonalOperationID(fmt.Sprintf("synthetic-request-%d", i), "reserve"), Kind: "reserve", UserID: 3, AgentUserID: 2, Model: "demo-model", ChannelID: 11, CustomerRatio: 900000, AgentRatio: 800000, BaseQuota: 100000, CustomerTarget: 90000, AgentTarget: 80000}
		_, err := model.PreConsumeBillingBalances(model.BillingBalancePreConsumeParams{PersonalFact: &fact, RequestId: fact.RequestID, UserId: 3, ModelName: fact.Model, Amount: fact.CustomerTarget, BillingSource: "wallet"})
		if err != nil {
			log.Fatal(err)
		}
		if i == 0 {
			fact.Kind, fact.OperationID = "settle", model.PersonalOperationID(fact.RequestID, "settle")
			encoded, err := common.Marshal(fact)
			if err != nil {
				log.Fatal(err)
			}
			journal := &model.TaskSettlementJournal{PersonalFact: string(encoded), RequestId: "synthetic-settle", Operation: model.TaskSettlementOperationSettle, UserId: 3, BillingSource: "wallet", ExpectedQuota: 90000, FinalQuota: 90000, LogDone: true}
			if _, err := model.EnsureTaskSettlementJournal(journal); err != nil {
				log.Fatal(err)
			}
			if _, _, err := model.ApplyTaskSettlementJournal(journal.RequestId); err != nil {
				log.Fatal(err)
			}
		}
		if i < 2 {
			if _, err := model.ProcessResellerEvents(100); err != nil {
				log.Fatal(err)
			}
		}
	}

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	viewer := func(c *gin.Context) int {
		kind, _ := c.Cookie("fixture_viewer")
		switch kind {
		case "agent":
			return 2
		case "customer":
			return 3
		case "ordinary":
			return 4
		default:
			return 1
		}
	}
	engine.GET("/fixture/:viewer", func(c *gin.Context) {
		c.SetCookie("fixture_viewer", c.Param("viewer"), 3600, "/", "", false, true)
		destination := "/reseller/agents"
		switch c.Param("viewer") {
		case "agent":
			destination = "/reseller/customers"
		case "customer":
			destination = "/wallet"
		case "ordinary":
			destination = "/dashboard"
		}
		c.Redirect(http.StatusFound, "http://127.0.0.1:3107"+destination)
	})
	engine.Use(func(c *gin.Context) { id := viewer(c); c.Set("id", id); c.Set("role", users[id-1].Role); c.Next() })
	engine.POST("/api/user/auth/refresh", func(c *gin.Context) {
		id := viewer(c)
		now := time.Now().Unix()
		c.JSON(200, gin.H{"success": true, "data": gin.H{"access_token": "synthetic-browser-token", "token_type": "Bearer", "access_expires_at": now + 3600, "user": gin.H{"id": id, "username": users[id-1].Username, "role": users[id-1].Role}, "session": gin.H{"sid": "synthetic-" + users[id-1].Username, "current": true, "login_method": "fixture", "ip": "127.0.0.1", "user_agent": "browser-QA", "created_at": now, "last_active_at": now, "expires_at": now + 3600}}})
	})
	engine.GET("/api/setup", func(c *gin.Context) { c.JSON(200, gin.H{"success": true, "data": gin.H{"status": true}}) })
	engine.GET("/api/status", controller.GetStatus)
	engine.GET("/api/user/topup/info", controller.GetTopUpInfo)
	engine.GET("/api/user/aff", controller.GetAffCode)
	engine.GET("/api/subscription/plans", controller.GetSubscriptionPlans)
	engine.GET("/api/subscription/self", controller.GetSubscriptionSelf)
	engine.GET("/api/notice", func(c *gin.Context) { c.JSON(200, gin.H{"success": true, "data": ""}) })
	engine.GET("/api/organization/self", func(c *gin.Context) { c.JSON(200, gin.H{"success": true, "data": nil}) })
	engine.GET("/api/security/completion", func(c *gin.Context) {
		user := users[viewer(c)-1]
		c.JSON(200, gin.H{"success": true, "data": gin.H{"required": true, "complete": true, "kind": user.ResellerKind, "email": user.Email, "email_verified": true, "mfa_enabled": true, "has_password": true, "activation": "active"}})
	})
	engine.GET("/api/user/self", func(c *gin.Context) {
		var user model.User
		if err := db.First(&user, viewer(c)).Error; err != nil {
			c.Status(500)
			return
		}
		c.JSON(200, gin.H{"success": true, "data": gin.H{"id": user.Id, "username": user.Username, "role": user.Role, "quota": user.Quota, "reseller_kind": user.ResellerKind}})
	})
	admin := engine.Group("/api/reseller/admin/agents", func(c *gin.Context) { c.Set("reseller_admin", true); c.Next() })
	admin.POST("", controller.CreateResellerAgent)
	admin.POST("/:id/activation", controller.ResendAccountActivation)
	admin.GET("", controller.ListResellerUsers)
	admin.GET("/:id", controller.GetResellerWorkspace)
	admin.PUT("/:id/config", controller.SaveResellerConfiguration)
	admin.POST("/:id/config/preview", controller.PreviewResellerConfiguration)
	admin.PUT("/:id/enabled", controller.ChangeResellerEnabled)
	admin.POST("/:id/quota-adjustments", controller.AdjustResellerQuota)
	admin.GET("/:id/quota-adjustments", controller.GetResellerAdjustments)
	self := engine.Group("/api/reseller/self/customers")
	self.POST("", controller.CreateResellerCustomer)
	self.POST("/:id/activation", controller.ResendAccountActivation)
	self.GET("", controller.ListResellerUsers)
	self.GET("/:id", controller.GetResellerWorkspace)
	self.PUT("/:id/config", controller.SaveResellerConfiguration)
	self.PUT("/:id/enabled", controller.ChangeResellerEnabled)
	self.POST("/:id/quota-adjustments", controller.AdjustResellerQuota)
	self.GET("/:id/quota-adjustments", controller.GetResellerAdjustments)
	engine.GET("/api/reseller/self/billing", controller.GetResellerBilling)
	engine.GET("/api/reseller/self/billing/export", controller.ExportResellerBilling)
	engine.GET("/api/reseller/self/procurement", controller.GetResellerProcurement)
	engine.GET("/api/reseller/self/procurement/export", controller.ExportResellerProcurement)
	events := engine.Group("/api/reseller/self/procurement/events", func(c *gin.Context) { c.Set("reseller_procurement", true); c.Next() })
	events.GET("", controller.GetResellerBilling)
	events.GET("/export", controller.ExportResellerBilling)
	engine.GET("/api/reseller/personal/ratios", controller.GetPersonalRatioRanges)
	engine.GET("/api/reseller/personal/history", controller.GetPersonalRatioHistory)
	engine.GET("/api/realtime-pricing/:subject/:id", controller.GetRealtimePricing)
	engine.PUT("/api/realtime-pricing/:subject/:id", controller.SaveRealtimePricing)
	engine.POST("/api/realtime-pricing/:subject/:id/preview", controller.PreviewRealtimePricing)
	engine.POST("/api/realtime-pricing-migration", controller.ChangeRealtimePricingMigration)
	engine.NoRoute(func(c *gin.Context) {
		log.Printf("UNIMPLEMENTED_FIXTURE_REQUEST %s %s", c.Request.Method, c.Request.URL.Path)
		c.JSON(404, gin.H{"success": false, "message": "Unimplemented fixture endpoint"})
	})
	server := &http.Server{Addr: "127.0.0.1:3108", Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() { <-signals; _ = server.Close() }()
	log.Print("Synthetic reseller browser fixture: http://127.0.0.1:3108/fixture/admin")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
