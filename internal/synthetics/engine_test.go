// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRejectsInvalidExecutionDefinitions(t *testing.T) {
	valid := `{"version":1,"accountGroupId":"123","tests":[{"name":"home","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"}]}}]}`
	cases := map[string]string{
		"unknown metadata":        strings.Replace(valid, `"version":1`, `"version":1,"password":"redacted"`, 1),
		"unknown threshold":       strings.Replace(valid, `"type":"http-server"`, `"type":"http-server","thresholds":{"maxResponseTim":5}`, 1),
		"wrong threshold family":  strings.Replace(valid, `"type":"http-server"`, `"type":"http-server","thresholds":{"maxLoss":0}`, 1),
		"negative threshold":      strings.Replace(valid, `"type":"http-server"`, `"type":"http-server","thresholds":{"maxResponseTime":-1}`, 1),
		"duplicate agent":         strings.Replace(valid, `{"agentId":"6"}`, `{"agentId":"6"},{"agentId":"6"}`, 1),
		"read only test identity": strings.Replace(valid, `"url":`, `"testId":"44","url":`, 1),
		"trailing document":       valid + `{}`,
		"wrong version":           strings.Replace(valid, `"version":1`, `"version":2`, 1),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "suite.json")
			if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(file); err == nil {
				t.Fatal("invalid suite was accepted")
			}
		})
	}
}

func TestLoadConfigDoesNotIgnoreContentPastSizeLimit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "suite.json")
	payload := `{"version":1,"accountGroupId":"123","tests":[{"name":"home","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"}]}}]}` + strings.Repeat(" ", 4<<20) + `{}`
	if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(file); err == nil {
		t.Fatal("oversized configuration was accepted")
	}
}

func TestMeasurementEvaluationPreservesMissingValuesAndNativeOutcomes(t *testing.T) {
	limit := 100.0
	cases := []struct {
		name, kind, payload, status string
		thresholds                  Thresholds
		ready                       bool
	}{
		{name: "native None is successful", kind: "http-server", payload: `{"responseCode":200,"errorType":"None"}`, status: "passed", ready: true},
		{name: "missing response is pending", kind: "http-server", payload: `{"errorType":"None"}`, status: "pending"},
		{name: "missing selected timing is pending", kind: "http-server", payload: `{"responseCode":200}`, thresholds: Thresholds{MaxTotalTime: &limit}, status: "pending"},
		{name: "explicit expected non2xx", kind: "http-server", payload: `{"responseCode":404}`, thresholds: Thresholds{ExpectedResponseCodes: []int{404}}, status: "passed", ready: true},
		{name: "known error finishes without timings", kind: "http-server", payload: `{"errorType":"Connection","errorDetails":"credential-secret"}`, thresholds: Thresholds{MaxTotalTime: &limit}, status: "failed", ready: true},
		{name: "missing loss is pending", kind: "agent-to-server", payload: `{"avgLatency":1}`, status: "pending"},
		{name: "zero loss exists", kind: "agent-to-server", payload: `{"loss":0}`, status: "passed", ready: true},
		{name: "loss fails without absent latency", kind: "agent-to-server", payload: `{"loss":100}`, thresholds: Thresholds{MaxLatency: &limit}, status: "failed", ready: true},
		{name: "missing latency is pending", kind: "agent-to-server", payload: `{"loss":0}`, thresholds: Thresholds{MaxLatency: &limit}, status: "pending"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			var row resultRow
			if err := json.Unmarshal([]byte(item.payload), &row); err != nil {
				t.Fatal(err)
			}
			result, ready, err := evaluateRow(TestConfig{Type: item.kind, Thresholds: item.thresholds}, row)
			if err != nil || ready != item.ready || result.Status != item.status {
				t.Fatalf("result=%+v ready=%v err=%v", result, ready, err)
			}
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "credential-secret") {
				t.Fatal("raw error details leaked")
			}
		})
	}
}

func TestRequiredAgentsCannotCombineDifferentRounds(t *testing.T) {
	var rows []resultRow
	if err := json.Unmarshal([]byte(`[{"agent":{"agentId":"6"},"roundId":10,"responseCode":200},{"agent":{"agentId":"7"},"roundId":11,"responseCode":200}]`), &rows); err != nil {
		t.Fatal(err)
	}
	if _, complete, err := evaluateRows(TestConfig{Type: "http-server"}, []string{"6", "7"}, rows); err != nil || complete {
		t.Fatalf("different rounds complete=%v err=%v", complete, err)
	}
	rows[0].RoundID = "11"
	if _, complete, err := evaluateRows(TestConfig{Type: "http-server"}, []string{"6", "7"}, rows); err != nil || !complete {
		t.Fatalf("same round complete=%v err=%v", complete, err)
	}
}

func TestPartialDuplicateRowsCannotLookPassed(t *testing.T) {
	for _, payload := range []string{
		`[{"agent":{"agentId":"6"},"roundId":10},{"agent":{"agentId":"6"},"roundId":10,"responseCode":200}]`,
		`[{"agent":{"agentId":"6"},"roundId":10,"responseCode":200},{"agent":{"agentId":"6"},"roundId":10}]`,
	} {
		var rows []resultRow
		if err := json.Unmarshal([]byte(payload), &rows); err != nil {
			t.Fatal(err)
		}
		outcomes, complete, err := evaluateRows(TestConfig{Type: "http-server"}, []string{"6"}, rows)
		if err != nil || complete || len(outcomes) != 1 || outcomes[0].Status != "pending" {
			t.Fatalf("partial rows lost pending status: %+v complete=%v err=%v", outcomes, complete, err)
		}
		result := TestResult{Status: "pending", Agents: outcomes}
		markExecutionError(&result, context.DeadlineExceeded)
		if result.Agents[0].Status != "timed-out" {
			t.Fatalf("agent appears passed after timeout: %+v", result)
		}
	}
}

func TestKnownFailureTakesPrecedenceOverPendingDuplicates(t *testing.T) {
	for _, payload := range []string{
		`[{"agent":{"agentId":"6"},"roundId":10},{"agent":{"agentId":"6"},"roundId":10,"responseCode":500}]`,
		`[{"agent":{"agentId":"6"},"roundId":10,"responseCode":500},{"agent":{"agentId":"6"},"roundId":10}]`,
	} {
		var rows []resultRow
		if err := json.Unmarshal([]byte(payload), &rows); err != nil {
			t.Fatal(err)
		}
		outcomes, complete, err := evaluateRows(TestConfig{Type: "http-server"}, []string{"6"}, rows)
		if err != nil || !complete || len(outcomes) != 1 || outcomes[0].Status != "failed" {
			t.Fatalf("failure precedence lost: %+v complete=%v err=%v", outcomes, complete, err)
		}
	}
}

func TestResultLinksPreserveAccountContextWithoutFollowingForeignOrigins(t *testing.T) {
	client, err := newAPIClient(Options{BaseURL: "https://api.thousandeyes.com", Token: "secret", AccountGroupID: "123"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v7/test-results/101/http-server"
	for _, value := range []string{"?cursor=next", "http-server?cursor=next", "https://api.thousandeyes.com/v7/test-results/101/http-server?cursor=next"} {
		got, err := client.resultURL(value, path)
		if err != nil || got != "https://api.thousandeyes.com/v7/test-results/101/http-server?aid=123&cursor=next" {
			t.Fatalf("relative pagination got=%s err=%v", got, err)
		}
	}
	for _, value := range []string{"https://external.invalid/v7/test-results/101/http-server", "https://user:pass@api.thousandeyes.com/v7/test-results/101/http-server", "/v7/test-results/102/http-server", "/v7/test-results/101/http-server#token"} {
		if _, err := client.resultURL(value, path); err == nil {
			t.Fatal("unsafe result link accepted")
		}
	}
}

func TestRetryDelayRemainsBoundedAfterManyRejections(t *testing.T) {
	client := apiClient{pollInterval: time.Millisecond}
	if got := client.retryDelay(nil, 10); got != 64*time.Millisecond {
		t.Fatalf("retry delay reset: %v", got)
	}
	if got := client.retryDelay(http.Header{"Retry-After": []string{"2"}}, 0); got < 2*time.Second {
		t.Fatalf("ignored Retry-After: %v", got)
	}
}

func TestPlatformLinkRetainsOnlyDocumentedNumericContext(t *testing.T) {
	link := sanitizeAppLink("https://app.thousandeyes.com/view/tests?testId=101&roundId=10&token=credential-secret#credential-secret")
	if link != "https://app.thousandeyes.com/view/tests?roundId=10&testId=101" {
		t.Fatalf("unsafe or incomplete sanitized link: %s", link)
	}
	for _, value := range []string{"https://app.thousandeyes.com/credential-secret", "https://external.invalid/view/tests?testId=1", "https://user:credential-secret@app.thousandeyes.com/view/tests"} {
		if got := sanitizeAppLink(value); got != "" {
			t.Fatalf("unsafe link preserved: %s", got)
		}
	}
}

func TestCanceledSuiteDoesNotSubmitRemoteWork(t *testing.T) {
	config := Config{Version: 1, AccountGroupID: "123", Tests: []TestConfig{{Name: "home", Type: "http-server", Config: map[string]json.RawMessage{"url": json.RawMessage(`"https://target.invalid"`), "agents": json.RawMessage(`[{"agentId":"6"}]`)}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, config, Options{BaseURL: "https://example.invalid", Token: "secret"})
	if err == nil || !report.Failed() || len(report.Tests) != 1 || report.Tests[0].Status != "error" {
		t.Fatalf("canceled suite report=%+v err=%v", report, err)
	}
}
