//go:build ignore

// Seed only a disposable loopback E2E database. The gateway itself uses its
// normal executable, routers, authentication, billing and recovery workers.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"github.com/fxamacker/cbor/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func main() {
	path := os.Getenv("SQLITE_PATH")
	if os.Getenv("RESELLER_E2E_FIXTURE") != "synthetic-only" || !filepath.IsAbs(path) || !strings.Contains(path, "reseller-e2e-") || os.Getenv("SQL_DSN") != "" {
		panic("fixture requires a runner-owned disposable SQLite path")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		panic("fixture refuses to overwrite an existing database")
	}
	common.InitEnv()
	must(common.InitRedisClient())
	must(model.InitDB())
	must(model.InitLogDB())
	defer model.CloseDB()
	model.InitOptionMap()
	password, err := common.Password2Hash(os.Getenv("RESELLER_E2E_PASSWORD"))
	must(err)
	var ids = map[string]int{}
	passkeys := map[string]any{}
	tokens := map[string]string{}
	for _, item := range []struct {
		Name, Kind string
		Role       int
	}{{"root", "", common.RoleRootUser}, {"admin", "", common.RoleAdminUser}, {"agent", "agent", common.RoleCommonUser}, {"customer", "agent_customer", common.RoleCommonUser}, {"ordinary", "", common.RoleCommonUser}, {"other-agent", "agent", common.RoleCommonUser}, {"pending", "", common.RoleCommonUser}} {
		user := model.User{Username: "e2e-" + item.Name, Password: password, Email: item.Name + "@e2e.invalid", DisplayName: "E2E " + item.Name, Role: item.Role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, ResellerKind: item.Kind, Quota: 10000000, AffCode: "e2e-" + item.Name}
		if item.Kind != "" {
			user.Quota = 0
		}
		must(model.DB.Create(&user).Error)
		ids[item.Name] = user.Id
		if item.Name != "pending" {
			must(model.DB.Create(&model.UserSecurityState{UserID: user.Id, VerifiedEmail: user.Email, VerifiedAt: time.Now().Unix()}).Error)
			must(model.DB.Create(&model.TwoFA{UserId: user.Id, Secret: "JBSWY3DPEHPK3PXP", IsEnabled: true}).Error)
			private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			must(err)
			public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: private.X.FillBytes(make([]byte, 32)), -3: private.Y.FillBytes(make([]byte, 32))})
			must(err)
			pkcs8, err := x509.MarshalPKCS8PrivateKey(private)
			must(err)
			credential := make([]byte, 32)
			_, err = rand.Read(credential)
			must(err)
			rpID := "localhost"
			must(model.DB.Create(&model.PasskeyCredential{UserID: user.Id, RPID: &rpID, CredentialID: base64.StdEncoding.EncodeToString(credential), PublicKey: base64.StdEncoding.EncodeToString(public), UserPresent: true, UserVerified: true, Attachment: "platform", AttestationType: "none"}).Error)
			passkeys[item.Name] = map[string]any{"credentialId": base64.StdEncoding.EncodeToString(credential), "isResidentCredential": true, "rpId": rpID, "privateKey": base64.StdEncoding.EncodeToString(pkcs8), "userHandle": base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(user.Id))), "signCount": 0}
		}
		if item.Kind != "" {
			identity := model.ResellerIdentity{UserID: user.Id, Kind: item.Kind, Activation: "active"}
			if item.Kind == "agent_customer" {
				identity.AgentUserID = ids["agent"]
			}
			must(model.DB.Create(&identity).Error)
			if item.Kind == "agent" {
				must(model.DB.Create(&model.ResellerAccount{UserID: user.Id, Enabled: true, Balance: 0}).Error)
			}
		}
	}
	_, err = model.AdjustResellerQuota(ids["root"], ids["agent"], model.ResellerQuotaInput{OperationID: "fixture-procurement", Mode: "add", Quota: 10000000, Reason: "Synthetic E2E procurement funding"})
	must(err)
	_, err = model.AdjustResellerQuota(ids["agent"], ids["customer"], model.ResellerQuotaInput{OperationID: "fixture-customer", Mode: "add", Quota: 10000000, Reason: "Synthetic E2E customer allocation"})
	must(err)
	for i := range 101 {
		mode := "add"
		if i%2 == 1 {
			mode = "subtract"
		}
		_, err = model.AdjustResellerQuota(ids["agent"], ids["customer"], model.ResellerQuotaInput{OperationID: fmt.Sprintf("fixture-page-%d", i), Mode: mode, Quota: 1, Reason: "Synthetic pagination boundary"})
		must(err)
	}
	for _, name := range []string{"root", "agent", "customer", "ordinary", "other-agent", "admin"} {
		if name == "agent" || name == "other-agent" {
			continue
		}
		key := name + strings.Repeat("x", 48-len(name))
		tokens[name] = key
		must(model.DB.Create(&model.Token{UserId: ids[name], Key: key, Name: "E2E consumption", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, Group: "default"}).Error)
	}
	baseURL := os.Getenv("RESELLER_E2E_UPSTREAM")
	channel := model.Channel{Name: "E2E controlled upstream", Type: 1, Status: common.ChannelStatusEnabled, Key: "synthetic-upstream-key", BaseURL: &baseURL, Models: "reseller-demo,hidden-demo", Group: "default"}
	must(model.DB.Create(&channel).Error)
	for _, name := range []string{"reseller-demo", "hidden-demo"} {
		must(model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: name, Group: "default", Enabled: true}).Error)
	}
	const pluginSource = `
export const meta={apiVersion:1,key:"reseller-e2e",name:"Isolated reseller E2E",version:"1.0.0",author:{name:"Synthetic fixture"},models:["reseller-task"],fetchMode:"per_task",routes:[{method:"POST",path:"/reseller-e2e/jobs",type:"submit",models:["reseller-task"],decode:"submit",render:"created"},{method:"GET",path:"/reseller-e2e/jobs/:task_id",type:"query",render:"status"}]};
export const native={submit(ctx){return {kind:"submit",model:ctx.body.value.model,requestBody:ctx.body.value};},created(ctx,task){return {task_id:task.task_id,status:task.status};},status(ctx,task){return {task_id:task.task_id,status:task.status};}};
export function buildSubmitRequest(ctx){return {method:"POST",url:ctx.baseUrl+"/task/jobs",body:ctx.requestBody};}
export function parseSubmitResponse(ctx,response){const body=response.body;const result={taskId:body.id,taskData:body};if(body.immediate)result.immediate={status:body.status,reason:"Synthetic terminal result"};return result;}
export function buildQueryRequest(ctx){return {method:"GET",url:ctx.baseUrl+"/task/jobs/"+ctx.taskId};}
export function parseTaskResult(ctx,body){return {status:body.status,progress:"100%",reason:"Synthetic terminal result",taskData:body};}
`
	hash := sha256.Sum256([]byte(pluginSource))
	must(model.DB.Create(&model.TaskPlugin{Key: "reseller-e2e", APIVersion: 1, Version: "1.0.0", Source: model.LongText(pluginSource), SourceHash: fmt.Sprintf("%x", hash), Enabled: true, Active: true, CreatedAt: time.Now().Unix()}).Error)
	setting := `{"task_plugin_key":"reseller-e2e"}`
	taskChannel := model.Channel{Name: "E2E controlled task upstream", Type: 61, Status: common.ChannelStatusEnabled, Key: "synthetic-task-key", BaseURL: &baseURL, Models: "reseller-task", Group: "default", Setting: &setting}
	must(model.DB.Create(&taskChannel).Error)
	must(model.DB.Create(&model.Ability{ChannelId: taskChannel.Id, Model: "reseller-task", Group: "default", Enabled: true}).Error)
	input := model.ResellerConfigInput{Items: []model.ResellerConfigItem{{Model: "reseller-demo", ChannelID: channel.Id, Authorized: true, Ratio: "0.8"}, {Model: "reseller-task", ChannelID: taskChannel.Id, Authorized: true, Ratio: "0.8"}}}
	_, err = model.SaveResellerConfig(ids["root"], ids["agent"], true, input)
	must(err)
	for i := range input.Items {
		input.Items[i].Ratio = "0.9"
	}
	_, err = model.SaveResellerConfig(ids["agent"], ids["customer"], false, input)
	must(err)
	for i := range input.Items {
		input.Items[i].Ratio = "1.2"
	}
	_, err = model.SaveResellerConfig(ids["root"], ids["ordinary"], true, input)
	must(err)
	for key, value := range map[string]string{"performance_setting.monitor_enabled": "false", "passkey.enabled": "true", "passkey.allow_insecure_origin": "true", "passkey.rp_id": "localhost", "passkey.origins": os.Getenv("RESELLER_E2E_ORIGIN"), "ModelPrice": `{"reseller-demo":0.0002,"hidden-demo":0.0002,"reseller-task":0.0002}`, "ModelRatio": `{"reseller-demo":1,"hidden-demo":1,"reseller-task":1}`, "GroupRatio": `{"default":1}`, "SMTPServer": "127.0.0.1", "SMTPPort": os.Getenv("RESELLER_E2E_SMTP_PORT"), "SMTPFrom": "gateway@e2e.invalid", "ServerAddress": os.Getenv("RESELLER_E2E_ORIGIN"), "PasswordLoginEnabled": "true", "GlobalApiRateLimitNum": "1000000", "GlobalWebRateLimitNum": "1000000"} {
		must(model.UpdateOption(key, value))
	}
	var databaseVersion string
	must(model.DB.Raw("SELECT sqlite_version()").Scan(&databaseVersion).Error)
	payload, err := common.Marshal(map[string]any{"database_version": databaseVersion, "users": ids, "channel_id": channel.Id, "passkeys": passkeys, "tokens": tokens})
	must(err)
	must(os.WriteFile(os.Getenv("RESELLER_E2E_MANIFEST"), payload, 0600))
	fmt.Println("disposable E2E fixture seeded")
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
