package common

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strings"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type EmailKind string

const (
	EmailNotice           EmailKind = "notice"
	EmailAccountSummary   EmailKind = "account-summary"
	EmailOperationsReport EmailKind = "operations-report"
	EmailEmergencyAlert   EmailKind = "emergency-alert"
	EmailRecovery         EmailKind = "recovery"
	EmailQuotaWarning     EmailKind = "quota-warning"
	EmailChannelStatus    EmailKind = "channel-status"
	EmailModelUpdate      EmailKind = "model-update"
	EmailChannelTest      EmailKind = "channel-test"
	EmailVerification     EmailKind = "verification-code"
	EmailPasswordRecovery EmailKind = "password-recovery"
	EmailSecuritySetup    EmailKind = "security-setup"
	EmailSecurityChange   EmailKind = "security-change"
	EmailDeliveryTest     EmailKind = "delivery-test"
)

// Each message kind owns its content layout. Only email-safe framing is shared.
// Templates are embedded and parsed once; rendering performs no network or DB I/O.
//
//go:embed mail_templates/*.html
var emailTemplateFiles embed.FS
var emailTemplates = template.Must(template.New("mail").ParseFS(emailTemplateFiles, "mail_templates/*.html"))
var emailTemplateKinds = map[EmailKind]struct{ Label, Accent, Preview string }{
	EmailNotice:           {"系统通知 · Notification", "#475569", "查看本次通知的具体内容与后续事项"},
	EmailAccountSummary:   {"账户摘要 · Account summary", "#4f46e5", "账户余额、消费与调用情况，一封邮件掌握"},
	EmailOperationsReport: {"运维日报 · Operations report", "#0f172a", "先查看运行风险，再查看当日统计与排障线索"},
	EmailEmergencyAlert:   {"应急告警 · Action required", "#b91c1c", "检测到需要管理员及时处理的运行异常"},
	EmailRecovery:         {"恢复通知 · Recovery", "#047857", "本邮件列出的事件已达到恢复条件"},
	EmailQuotaWarning:     {"额度预警 · Quota warning", "#b45309", "请检查可用额度，提前避免调用中断"},
	EmailChannelStatus:    {"渠道状态 · Channel status", "#1d4ed8", "渠道状态发生变化，请核对原因及承接能力"},
	EmailModelUpdate:      {"模型变更 · Model update", "#6d28d9", "检查上游模型变化及需要处理的事项"},
	EmailChannelTest:      {"渠道检测 · Channel test", "#0369a1", "渠道检测执行结果与后续排查提示"},
	EmailVerification:     {"邮箱确认 · Email verification", "#4338ca", "请在有效期内使用验证码，切勿转发或分享"},
	EmailPasswordRecovery: {"账户恢复 · Account recovery", "#9a3412", "仅在本人发起时继续账户恢复操作"},
	EmailSecuritySetup:    {"安全设置 · Security setup", "#334155", "确认邮箱并完成账户安全设置"},
	EmailSecurityChange:   {"安全提醒 · Security notification", "#9f1239", "核对账户安全变更是否由本人发起"},
	EmailDeliveryTest:     {"投递测试 · Delivery test", "#0369a1", "这是一封邮件配置测试，不需要操作账户"},
}

func RenderEmailTemplate(kind EmailKind, subject, content string) (string, error) {
	return renderEmailTemplate(kind, subject, content, nil)
}

func renderEmailTemplate(kind EmailKind, subject, content string, copy *SecurityLinkEmailCopy) (string, error) {
	if strings.ContainsAny(subject, "\r\n") {
		return "", fmt.Errorf("invalid email subject")
	}
	meta, ok := emailTemplateKinds[kind]
	if !ok {
		return "", fmt.Errorf("unknown email template")
	}
	body, err := safeEmailContent(content)
	if err != nil {
		return "", err
	}
	name := string(kind)
	if copy == nil {
		copy = &SecurityLinkEmailCopy{Label: meta.Label, Preview: meta.Preview}
	} else {
		name = "security-link"
	}
	data := struct {
		SecurityLinkEmailCopy
		Kind                   EmailKind
		Subject, Brand, Accent string
		Body                   template.HTML
	}{*copy, kind, subject, SystemName, meta.Accent, body}
	var output bytes.Buffer
	if err = emailTemplates.ExecuteTemplate(&output, name, data); err != nil {
		return "", fmt.Errorf("email template could not be rendered")
	}
	return output.String(), nil
}

// Existing send APIs retain a generic notification template for future callers;
// known business messages select their kind explicitly instead of guessing by subject.
func SendEmail(subject, receiver, content string) error {
	return SendNotificationEmail(EmailNotice, subject, receiver, content)
}
func SendNotificationEmail(kind EmailKind, subject, receiver, content string) error {
	rendered, err := RenderEmailTemplate(kind, subject, content)
	if err != nil {
		return err
	}
	return sendEmail(subject, receiver, rendered)
}
func DeliverConfiguredEmail(ctx context.Context, config EmailDeliveryConfig, subject, receiver, content string) (EmailDeliveryReport, error) {
	return DeliverNotificationEmail(ctx, config, EmailNotice, subject, receiver, content)
}
func DeliverNotificationEmail(ctx context.Context, config EmailDeliveryConfig, kind EmailKind, subject, receiver, content string) (EmailDeliveryReport, error) {
	rendered, err := RenderEmailTemplate(kind, subject, content)
	if err != nil {
		return EmailDeliveryReport{Code: "EMAIL_DELIVERY_FAILED"}, err
	}
	return deliverConfiguredEmail(ctx, config, subject, receiver, rendered)
}

// Existing message builders contain small HTML fragments, including security
// links and codes. Preserve their text/links while excluding active content,
// remote resources, event handlers and unsafe schemes. Never log message bodies.
func safeEmailContent(content string) (template.HTML, error) {
	truncated := len(content) > 64<<10
	if truncated {
		content = strings.ToValidUTF8(content[:64<<10], "�")
	}
	nodes, err := nethtml.ParseFragment(strings.NewReader(content), &nethtml.Node{Type: nethtml.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return "", fmt.Errorf("email content could not be rendered")
	}
	var output strings.Builder
	for _, node := range nodes {
		writeEmailNode(&output, node)
	}
	if truncated {
		output.WriteString("<p>内容已截短，请在应用内查看详情。 / Content shortened; view details in the application.</p>")
	}
	return template.HTML(output.String()), nil // #nosec G203 -- allowlisted tree, attributes and schemes only.
}
func writeEmailNode(output *strings.Builder, node *nethtml.Node) {
	if node.Type == nethtml.TextNode {
		output.WriteString(html.EscapeString(node.Data))
		return
	}
	if node.Type != nethtml.ElementNode {
		return
	}
	switch node.Data {
	case "script", "style", "iframe", "object", "embed", "svg", "math", "form", "input", "button", "img", "video", "audio", "link", "meta":
		return
	}
	allowed := false
	switch node.Data {
	case "p", "br", "strong", "b", "em", "i", "a", "code", "pre", "ul", "ol", "li", "table", "thead", "tbody", "tr", "td", "th", "h1", "h2", "h3", "h4", "blockquote", "div", "span", "hr":
		allowed = true
	}
	if allowed {
		output.WriteByte('<')
		output.WriteString(node.Data)
		if node.Data == "a" {
			for _, attr := range node.Attr {
				if attr.Key != "href" {
					continue
				}
				link, err := url.Parse(strings.TrimSpace(attr.Val))
				if err == nil && (link.Scheme == "https" || link.Scheme == "http") && link.Host != "" && link.User == nil {
					output.WriteString(" href=\"")
					output.WriteString(html.EscapeString(attr.Val))
					output.WriteString("\" rel=\"noreferrer noopener\"")
				}
				break
			}
		}
		output.WriteByte('>')
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writeEmailNode(output, child)
	}
	if allowed && node.Data != "br" && node.Data != "hr" {
		output.WriteString("</")
		output.WriteString(node.Data)
		output.WriteByte('>')
	}
}
