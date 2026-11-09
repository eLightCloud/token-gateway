//go:build ignore

// Used only by notifications_e2e.mjs against its disposable local SMTP server.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func main() {
	host, portText, err := net.SplitHostPort(os.Getenv("NOTIFICATIONS_E2E_SMTP"))
	if err != nil || host != "127.0.0.1" {
		panic("fixture requires runner-owned loopback SMTP")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		panic(err)
	}
	if os.Getenv("NOTIFICATIONS_E2E_MODE") == "seed" {
		seedNotificationsDatabase(port)
		return
	}
	config := common.EmailDeliveryConfig{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthLegacy, Legacy: common.LegacyEmailSettings{Host: host, Port: port, From: "gateway@e2e.invalid"}}}
	common.SystemName = "new-api 通知测试"
	common.PublishEmailDelivery(config, nil)
	for i, item := range []struct {
		Kind          common.EmailKind
		Subject, Body string
	}{
		{common.EmailNotice, "系统通知", "<p>本次系统通知的具体内容。</p>"},
		{common.EmailAccountSummary, "11 月 5 日账户摘要", "<p>统计区间：昨日 00:00–24:00（北京时间）</p><table><tr><th>消费</th><td>¥12.34</td></tr><tr><th>当前余额</th><td>¥8.50</td></tr><tr><th>请求 / 错误</th><td>128 / 3</td></tr></table><p>余额存在风险，请及时查看账户。</p>"},
		{common.EmailOperationsReport, "11 月 5 日运维日报", "<h2>运行情况</h2><p>调用 12,800 次，错误 18 次，影响 6 位用户。</p><h2>主要错误</h2><p>上游超时：12；使用错误：6。详见应用内使用与审计日志。</p>"},
		{common.EmailEmergencyAlert, "资源持续超限", "<p>节点：synthetic-node</p><p>CPU 85%（阈值 80%），内存 75%（阈值 70%），磁盘 92%（阈值 90%）。</p><p>持续时间：5 分钟；请检查应用内资源情况。</p>"},
		{common.EmailRecovery, "资源告警已恢复", "<p>节点：synthetic-node</p><p>CPU 已回落至 35%，达到恢复条件。</p>"},
		{common.EmailQuotaWarning, "账户额度不足提醒", "<p>当前可用额度：¥8.50。请及时查看账户余额。</p>"},
		{common.EmailChannelStatus, "渠道已自动禁用", "<p>渠道 synthetic-provider 已禁用。</p><p>原因：上游返回无效凭据。请检查渠道配置。</p>"},
		{common.EmailModelUpdate, "上游模型列表发生变化", "<p>渠道：synthetic-provider</p><ul><li>新增模型：demo-model</li><li>移除模型：old-demo</li></ul>"},
		{common.EmailChannelTest, "渠道检测完成", "<p>检查 5 个渠道，成功 4 个，失败 1 个。</p>"},
		{common.EmailVerification, "邮箱验证邮件", "<p>您的验证码为：<strong>482731</strong></p><p>验证码 10 分钟内有效。</p>"},
		{common.EmailPasswordRecovery, "账户密码恢复", "<p>恢复链接 10 分钟内有效。</p><a href=\"https://gateway.example/account-activation#token=synthetic-token&amp;purpose=security_password_reset\">继续恢复密码</a>"},
		{common.EmailSecuritySetup, "完成安全设置", "<p>请确认邮箱并完成安全设置。</p><a href=\"https://gateway.example/account-activation#token=synthetic-setup\">继续安全设置</a>"},
		{common.EmailSecurityChange, "账户安全信息已变更", "<p>变更：密码已更新。</p><p>操作时间：2026-11-06 06:10（北京时间）</p><p>如果不是本人操作，请进入应用检查账户。</p>"},
		{common.EmailDeliveryTest, "邮件投递测试", "<p>保存的邮件配置已成功完成投递。</p>"},
	} {
		body := item.Body + "<p>长文本排版检查：" + strings.Repeat("x", 200) + "</p><script>window.injected=true</script><img src=\"https://tracking.invalid/pixel\">"
		if i%2 == 0 {
			err = common.SendNotificationEmail(item.Kind, item.Subject, "recipient@e2e.invalid", body)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err = common.DeliverNotificationEmail(ctx, config, item.Kind, item.Subject, "recipient@e2e.invalid", body)
			cancel()
		}
		if err != nil {
			panic(err)
		}
		fmt.Println(item.Kind)
	}
}

func seedNotificationsDatabase(port int) {
	path := os.Getenv("SQLITE_PATH")
	if !filepath.IsAbs(path) || !strings.Contains(path, "notifications-e2e-") || os.Getenv("SQL_DSN") != "" || os.Getenv("LOG_SQL_DSN") != "" {
		panic("fixture requires runner-owned disposable SQLite")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		panic("fixture refuses to overwrite existing database")
	}
	common.InitEnv()
	for _, init := range []func() error{common.InitRedisClient, model.InitDB, model.InitLogDB} {
		if err := init(); err != nil {
			panic(err)
		}
	}
	defer model.CloseDB()
	model.InitOptionMap()
	user := model.User{Username: "notifications-e2e", Email: "recipient@e2e.invalid", DisplayName: "Synthetic account", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, Quota: 10000000, AffCode: "notifications-e2e"}
	if err := model.DB.Create(&user).Error; err != nil {
		panic(err)
	}
	if err := model.DB.Create(&model.UserSecurityState{UserID: user.Id, VerifiedEmail: user.Email, VerifiedAt: time.Now().Unix()}).Error; err != nil {
		panic(err)
	}
	silent := model.User{Username: "notifications-silent", Email: "silent@e2e.invalid", Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, AffCode: "notifications-silent"}
	if err := model.DB.Create(&silent).Error; err != nil {
		panic(err)
	}
	if err := model.DB.Create(&model.Token{UserId: user.Id, Key: strings.Repeat("n", 48), Name: "Synthetic relay", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, Group: "default"}).Error; err != nil {
		panic(err)
	}
	upstream := os.Getenv("NOTIFICATIONS_E2E_UPSTREAM")
	if !strings.HasPrefix(upstream, "http://127.0.0.1:") {
		panic("fixture requires loopback upstream")
	}
	channel := model.Channel{Name: "Synthetic notification upstream", Type: 1, Status: common.ChannelStatusEnabled, Key: "synthetic-key", BaseURL: &upstream, Models: "notifications-demo", Group: "default"}
	if err := model.DB.Create(&channel).Error; err != nil {
		panic(err)
	}
	if err := model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: "notifications-demo", Group: "default", Enabled: true}).Error; err != nil {
		panic(err)
	}
	if err := model.SaveOperationsSettings(context.Background(), model.OperationsSettings{DailyEnabled: true, PreviewOnly: true, Recipients: "admin@e2e.invalid"}); err != nil {
		panic(err)
	}
	if _, err := model.SaveEmailDeliveryConfig(common.EmailDeliveryUpdate{Intent: common.EmailDeliveryIntent{Options: common.EmailDeliveryOptions{AuthMode: common.EmailAuthLegacy, Legacy: common.LegacyEmailSettings{Host: "127.0.0.1", Port: port, From: "gateway@e2e.invalid"}}, LegacyPasswordAction: "keep", ClientSecretAction: "keep"}}); err != nil {
		panic(err)
	}
	for key, value := range map[string]string{"performance_setting.monitor_enabled": "false", "ServerAddress": "http://localhost:" + os.Getenv("PORT"), "ModelPrice": `{"notifications-demo":0.0002}`, "GroupRatio": `{"default":1}`} {
		if err := model.UpdateOption(key, value); err != nil {
			panic(err)
		}
	}
}
