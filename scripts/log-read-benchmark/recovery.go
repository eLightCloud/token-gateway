package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func chainAccount(fixture model.Token) (model.User, model.Token) {
	var user model.User
	var token model.Token
	must(model.DB.First(&user, fixture.UserId).Error)
	must(model.DB.First(&token, fixture.Id).Error)
	return user, token
}

func checkChainAccounting(tokens []model.Token, channelID int, expected map[string]model.Token, missingLogs, pendingJournals int) {
	var rows []model.Log
	must(model.LOG_DB.Where("channel_id >= ? AND type = ?", channelID, model.LogTypeConsume).Find(&rows).Error)
	verify(len(rows)+missingLogs == len(expected), "accepted=%d logged=%d expected_missing_logs=%d", len(expected), len(rows), missingLogs)
	seen := make(map[string]bool, len(rows))
	calls := make(map[int]int, len(tokens))
	byChannel := make(map[int]int64)
	for _, token := range expected {
		calls[token.Id]++
	}
	for _, row := range rows {
		token := expected[row.RequestId]
		verify(token.Id != 0 && !seen[row.RequestId], "unmatched or duplicate log for %q", row.RequestId)
		verify(row.BillingOperationId != nil && *row.BillingOperationId == "settle:"+row.RequestId, "incorrect operation for %q", row.RequestId)
		verify(row.UserId == token.UserId && row.TokenId == token.Id && row.Quota == chainCharge &&
			row.PromptTokens == chainPrompt && row.CompletionTokens == chainCompletion, "incorrect log owner, quota or token counts for %q", row.RequestId)
		seen[row.RequestId] = true
		byChannel[row.ChannelId] += int64(row.Quota)
	}
	for _, fixture := range tokens {
		user, token := chainAccount(fixture)
		count := calls[token.Id]
		charge := int64(count * chainCharge)
		verify(user.RequestCount == count && user.UsedQuota == charge && chainBalance-user.Quota == charge &&
			token.UsedQuota == charge && chainBalance-token.RemainQuota == charge,
			"account %d expected %d calls / %d debit, got calls=%d user_used=%d wallet_debit=%d token_used=%d token_debit=%d",
			user.Id, count, charge, user.RequestCount, user.UsedQuota, chainBalance-user.Quota, token.UsedQuota, chainBalance-token.RemainQuota)
	}
	var channels []model.Channel
	must(model.DB.Where("id >= ?", channelID).Find(&channels).Error)
	var channelQuota int64
	for _, channel := range channels {
		channelQuota += channel.UsedQuota
		if missingLogs == 0 {
			verify(channel.UsedQuota == byChannel[channel.Id], "channel %d debit did not match its usage logs", channel.Id)
		}
	}
	verify(channelQuota == int64(len(expected)*chainCharge), "channel debit did not match accepted calls")
	journals, err := model.ListPendingTaskSettlementJournals(len(expected) + 1)
	must(err)
	verify(len(journals) == pendingJournals, "expected %d pending journals, got %d", pendingJournals, len(journals))
	fmt.Printf("CHAIN_ACCOUNTING accepted=%d logged=%d wallet_debit=%d token_debit=%d channel_debit=%d pending=%d\n",
		len(expected), len(rows), len(expected)*chainCharge, len(expected)*chainCharge, channelQuota, len(journals))
	if len(channels) > 1 {
		fmt.Printf("CHAIN_CHANNELS configured=%d active=%d quota_by_channel=%v\n", len(channels), len(byChannel), byChannel)
	}
}

func checkChainFault(gateway chainGateway, tokens []model.Token, channelID int, upstreamCalls *atomic.Int64, phase string) {
	first, second, third := tokens[0], tokens[1], tokens[2]
	if phase == "journal-gap" {
		must(model.DB.Exec(`CREATE FUNCTION bench_reject_settle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture settlement journal unavailable' USING ERRCODE = '55000'; END;
$$`).Error)
		must(model.DB.Exec(`CREATE TRIGGER bench_reject_settle BEFORE INSERT ON task_settlement_journals
FOR EACH ROW WHEN (NEW.operation = 'settle') EXECUTE FUNCTION bench_reject_settle()`).Error)
		unkeyed := gateway.relay(first)
		gateway.idempotencyKey = "benchmark-journal-gap"
		response := gateway.relay(second)
		must(model.DB.Exec("DROP TRIGGER bench_reject_settle ON task_settlement_journals").Error)
		retry := gateway.relay(second)
		replayed := service.SweepPendingTaskSettlements(context.Background())
		var logs int64
		must(model.LOG_DB.Model(&model.Log{}).Where("request_id = ? AND type = ?", response.id, model.LogTypeConsume).Count(&logs).Error)
		journals, err := model.ListPendingTaskSettlementJournals(10)
		must(err)
		user, token := chainAccount(second)
		fmt.Printf("CHAIN_JOURNAL_GAP status=%d accepted=%t upstream=%d logs_after_replay=%d pending_after_replay=%d replayed=%d request_count=%d wallet_debit=%d token_debit=%d\n",
			response.status, response.accepted(), upstreamCalls.Load(), logs, len(journals), replayed, user.RequestCount, chainBalance-user.Quota, chainBalance-token.RemainQuota)
		verify(unkeyed.err == nil && unkeyed.status == http.StatusServiceUnavailable && response.err == nil &&
			response.status == http.StatusServiceUnavailable && !unkeyed.accepted() && !response.accepted(), "journal fault must suppress both success responses")
		verify(retry.err == nil && retry.status == response.status && retry.id == response.id && bytes.Equal(retry.body, response.body) && upstreamCalls.Load() == 2,
			"failed keyed operation must replay the original failure without an upstream call")
		checkChainAccounting(tokens, channelID, nil, 0, 0)
		return
	}

	must(model.LOG_DB.Exec(`CREATE FUNCTION bench_reject_log() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture log insert unavailable' USING ERRCODE = '55000'; END;
$$`).Error)
	must(model.LOG_DB.Exec(`CREATE TRIGGER bench_reject_log BEFORE INSERT ON logs
FOR EACH ROW WHEN (NEW.billing_operation_id IS NOT NULL) EXECUTE FUNCTION bench_reject_log()`).Error)
	gateway.idempotencyKey = "benchmark-log-delivery-fault"
	missing := gateway.relay(first)
	verify(missing.accepted(), "log rejection response status=%d err=%v body=%s", missing.status, missing.err, missing.body)
	replayed := gateway.relay(first)
	verify(replayed.accepted() && replayed.id == missing.id && bytes.Equal(replayed.body, missing.body) && upstreamCalls.Load() == 1,
		"durable settlement with pending log did not replay the original response")
	gateway.idempotencyKey = ""
	must(model.LOG_DB.Exec("DROP TRIGGER bench_reject_log ON logs").Error)

	must(model.DB.Exec(`CREATE FUNCTION bench_reject_ack() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture log acknowledgement unavailable' USING ERRCODE = '55000'; END;
$$`).Error)
	must(model.DB.Exec(`CREATE TRIGGER bench_reject_ack BEFORE UPDATE ON task_settlement_journals
FOR EACH ROW WHEN (NEW.log_done AND NOT OLD.log_done) EXECUTE FUNCTION bench_reject_ack()`).Error)
	committed := gateway.relay(second)
	verify(committed.accepted(), "ack rejection response status=%d err=%v body=%s", committed.status, committed.err, committed.body)
	must(model.DB.Exec("DROP TRIGGER bench_reject_ack ON task_settlement_journals").Error)

	must(model.DB.Exec(`CREATE FUNCTION bench_reject_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'fixture journal cleanup unavailable' USING ERRCODE = '55000'; END;
$$`).Error)
	must(model.DB.Exec(`CREATE TRIGGER bench_reject_cleanup BEFORE DELETE ON task_settlement_journals
FOR EACH ROW WHEN (OLD.operation = 'settle') EXECUTE FUNCTION bench_reject_cleanup()`).Error)
	cleanup := gateway.relay(third)
	verify(cleanup.accepted(), "cleanup rejection response status=%d err=%v body=%s", cleanup.status, cleanup.err, cleanup.body)
	must(model.DB.Exec("DROP TRIGGER bench_reject_cleanup ON task_settlement_journals").Error)

	expected := map[string]model.Token{missing.id: first, committed.id: second, cleanup.id: third}
	journals, err := model.ListPendingTaskSettlementJournals(10)
	must(err)
	verify(len(journals) == len(expected) && upstreamCalls.Load() == 3, "each fault must leave its journal pending")
	for _, journal := range journals {
		id, ok := strings.CutPrefix(journal.RequestId, "settle:")
		verify(ok && expected[id].Id == journal.TokenId && journal.Applied && !journal.LogDone && journal.LogPayload != "" &&
			journal.ExpectedQuota > 0 && journal.FinalQuota == chainCharge, "invalid durable recovery record")
		var count int64
		must(model.LOG_DB.Model(&model.Log{}).Where("request_id = ?", id).Count(&count).Error)
		if id == missing.id {
			verify(count == 0, "log insert fault should leave no log before restart")
		} else {
			verify(count == 1, "ack fault should leave one log before restart")
		}
	}
	var task *model.SystemTask
	task, err = model.GetActiveSystemTask(model.SystemTaskTypeBillingRecovery)
	must(err)
	verify(task != nil && task.Status == model.SystemTaskStatusPending, "faults should enqueue a durable recovery job")
	checkChainAccounting(tokens, channelID, expected, 1, 3)
	fmt.Println("CHAIN_FAULT log_insert_rejected=1 log_ack_rejected=1 journal_cleanup_rejected=1 accepted=3 durable_applied_journals=3 queued_recovery_tasks=1")
}

func recoverFullChain(tokens []model.Token, channelID int) {
	journals, err := model.ListPendingTaskSettlementJournals(10)
	must(err)
	verify(len(journals) == 3, "fresh process must see all three pending journals")
	expected := make(map[string]model.Token, len(journals))
	for _, journal := range journals {
		id, ok := strings.CutPrefix(journal.RequestId, "settle:")
		verify(ok && journal.Applied && !journal.LogDone, "fresh process could not read recovery state")
		for _, token := range tokens {
			if token.Id == journal.TokenId {
				expected[id] = token
			}
		}
	}
	verify(len(expected) == len(journals), "pending operations belong to unknown tokens")
	task, err := model.GetActiveSystemTask(model.SystemTaskTypeBillingRecovery)
	must(err)
	verify(task != nil, "fresh process must see queued recovery task")

	common.IsMasterNode = true
	controller.RegisterScheduledSystemTasks()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.RunSystemTaskRunner(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for task.Status != model.SystemTaskStatusSucceeded {
		select {
		case <-ctx.Done():
			panic("recovery runner did not finish its queued task")
		case <-ticker.C:
			must(model.DB.First(task, task.ID).Error)
		}
	}
	var result struct {
		Recovered int `json:"recovered_settlements"`
	}
	must(common.UnmarshalJsonStr(task.Result, &result))
	verify(result.Recovered == len(expected), "runner recovered %d, expected %d", result.Recovered, len(expected))
	cancel()
	<-done
	secondSweep := service.SweepPendingTaskSettlements(context.Background())
	verify(secondSweep == 0, "a further recovery pass replayed %d completed operations", secondSweep)
	checkChainAccounting(tokens, channelID, expected, 0, 0)
	fmt.Printf("CHAIN_RECOVERY restarted=true runner_recovered=%d repeated_sweep=%d elapsed=%s\n", result.Recovered, secondSweep, time.Since(start))
}

func checkClientRetry(gateway chainGateway, tokens []model.Token, channelID int, upstreamCalls *atomic.Int64, restarted bool) {
	gateway.idempotencyKey = "benchmark-client-retry"
	first := gateway.relay(tokens[0])
	second := gateway.relay(tokens[0])
	verify(first.accepted() && second.accepted() && bytes.Equal(first.body, second.body), "retry fixture expected the same successful response")
	expected := map[string]model.Token{first.id: tokens[0], second.id: tokens[0]}
	checkChainAccounting(tokens, channelID, expected, 0, 0)
	changed := gateway.call(http.MethodPost, "/v1/chat/completions", "sk-"+tokens[0].Key, []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"different"}]}`))
	verify(changed.err == nil && changed.status == http.StatusConflict && bytes.Contains(changed.body, []byte("idempotency_key_conflict")), "different request reused the same key")
	wantUpstream := int64(1)
	if restarted {
		wantUpstream = 0
	}
	fmt.Printf("CHAIN_CLIENT_RETRY restarted=%t same_idempotency_key=true same_gateway_request_id=%t upstream=%d logged=%d wallet_debit=%d conflict=%d\n",
		restarted, first.id == second.id, upstreamCalls.Load(), len(expected), len(expected)*chainCharge, changed.status)
	verify(len(expected) == 1 && upstreamCalls.Load() == wantUpstream, "retry of a successful HTTP request was billed twice")
}
