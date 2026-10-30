// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeSyntheticConfig(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetics.json")
	if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func executeSyntheticSuite(t *testing.T, baseURL, payload string, extra ...string) (string, string, error) {
	t.Helper()
	t.Setenv("TE_TOKEN", "synthetics-test-token")
	t.Setenv("TE_BASE_URL", baseURL)
	command := newTestRootCommand(t)
	stdout, stderr := captureCommandIO(t, command)
	args := []string{"synthetics", "run-tests", "--config", writeSyntheticConfig(t, payload)}
	command.SetArgs(append(args, extra...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func syntheticJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("write JSON: %v", err)
	}
}

func TestSyntheticsHTTPWaitsForAllAgentsAndWritesReports(t *testing.T) {
	var polls, creates atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetics-test-token" {
			t.Error("missing bearer authentication")
		}
		if r.URL.Query().Get("aid") != "123" {
			t.Errorf("missing account-group context in %s", r.URL.Path)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v7/tests/http-server/instant":
			creates.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["url"] != "https://target.invalid" {
				t.Errorf("unexpected target: %v", body["url"])
			}
			syntheticJSON(t, w, http.StatusCreated, map[string]any{
				"testId": "101", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}, map[string]any{"agentId": "7", "agentType": "cloud"}},
				"_links": map[string]any{"testResults": []any{map[string]any{"href": server.URL + "/v7/test-results/101/http-server"}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v7/test-results/101/http-server":
			row := map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "responseTime": 30, "totalTime": 40, "errorType": "None", "headers": map[string]any{"requestHeaders": "secret-request-header"}}
			if r.URL.Query().Get("cursor") == "second" {
				rows := []any{}
				if polls.Load() >= 2 {
					row["agent"] = map[string]any{"agentId": "7"}
					rows = append(rows, row)
				}
				syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "101"}, "results": rows})
				return
			}
			polls.Add(1)
			syntheticJSON(t, w, http.StatusOK, map[string]any{
				"test": map[string]any{"testId": "101"}, "results": []any{row},
				"_links": map[string]any{"next": map[string]any{"href": server.URL + "/v7/test-results/101/http-server?cursor=second"}},
			})
		default:
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reportPath := filepath.Join(t.TempDir(), "result.json")
	junitPath := filepath.Join(t.TempDir(), "result.xml")
	stdout, stderr, err := executeSyntheticSuite(t, server.URL, `{"version":1,"accountGroupId":"123","timeout":"1s","pollInterval":"1ms","tests":[{"name":"homepage","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"},{"agentId":"7"}]},"thresholds":{"maxResponseTime":100}}]}`, "--json-report", reportPath, "--junit-report", junitPath)
	if err != nil {
		t.Fatalf("expected passing suite: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if creates.Load() != 1 || polls.Load() < 2 {
		t.Fatalf("expected one isolated creation and repeated polling; creates=%d polls=%d", creates.Load(), polls.Load())
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Tests []struct {
			Status string `json:"status"`
			Agents []struct {
				AgentID string `json:"agentId"`
			} `json:"agents"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Tests) != 1 || report.Tests[0].Status != "passed" || len(report.Tests[0].Agents) != 2 {
		t.Fatalf("unexpected sanitized report: %s", data)
	}
	for _, sensitive := range []string{"synthetics-test-token", "secret-request-header"} {
		if strings.Contains(string(data)+stdout+stderr, sensitive) {
			t.Errorf("output exposed %q", sensitive)
		}
	}
	xml, err := os.ReadFile(junitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xml), "<testsuite") || !strings.Contains(string(xml), "<testcase") {
		t.Fatalf("missing JUnit test suite or cases: %s", xml)
	}
}

func TestSyntheticsMissingLossCannotPass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "102", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "102"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "avgLatency": 1}}})
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, `{"version":1,"accountGroupId":"123","timeout":"50ms","pollInterval":"1ms","tests":[{"name":"network","type":"agent-to-server","config":{"server":"target.invalid:443","protocol":"tcp","agents":[{"agentId":"6"}]}}]}`)
	if err == nil {
		t.Fatal("missing loss measurement passed the CI gate")
	}
}

func TestSyntheticsValidatesWholeSuiteBeforeCreatingTests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, `{"version":1,"accountGroupId":"123","tests":[{"name":"valid","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"}]}},{"name":"unsupported","type":"bgp","config":{"server":"target.invalid","agents":[{"agentId":"6"}]}}]}`)
	if err == nil {
		t.Fatal("expected unsupported type validation error")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid suite created remote work: %d requests", calls.Load())
	}
}

func singleHTTPConfig(rule string) string {
	return `{"version":1,"accountGroupId":"123","timeout":"1s","pollInterval":"1ms","tests":[{"name":"http-check","type":"http-server","executionRule":"` + rule + `","config":{"url":"https://target.invalid","agents":[{"agentId":"6"}]}}]}`
}

func TestSyntheticsExecutionRules(t *testing.T) {
	for _, tc := range []struct {
		rule       string
		wantError  bool
		wantStatus string
		wantCalls  int32
	}{
		{"blocking", true, "failed", 1},
		{"non-blocking", false, "failed", 1},
		{"skipped", false, "skipped", 0},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			var creates atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					creates.Add(1)
					syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "103", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
					return
				}
				syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "103"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 503, "errorType": "None"}}})
			}))
			defer server.Close()
			stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig(tc.rule), "--json")
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v\nstdout:%s\nstderr:%s", err, tc.wantError, stdout, stderr)
			}
			if got := creates.Load(); got != tc.wantCalls {
				t.Fatalf("created %d instant tests, want %d", got, tc.wantCalls)
			}
			var report struct {
				Tests []struct {
					Status string `json:"status"`
				} `json:"tests"`
			}
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
			}
			if len(report.Tests) != 1 || report.Tests[0].Status != tc.wantStatus {
				t.Fatalf("unexpected report status: %s", stdout)
			}
		})
	}
}

func TestSyntheticsAPIErrorBodiesAreNotExposed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		syntheticJSON(t, w, http.StatusUnauthorized, map[string]any{"detail": "private-api-body synthetics-test-token https://private-error.invalid"})
	}))
	defer server.Close()
	stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("non-blocking"), "--json")
	if err == nil {
		t.Fatal("authentication failure cannot become a non-blocking test failure")
	}
	for _, secret := range []string{"private-api-body", "synthetics-test-token", "https://private-error.invalid"} {
		if strings.Contains(stdout+stderr+err.Error(), secret) {
			t.Errorf("exposed API error contents: %q", secret)
		}
	}
}

func TestSyntheticsDoesNotSendBearerToExternalLinks(t *testing.T) {
	for _, linkKind := range []string{"results", "pagination"} {
		t.Run(linkKind, func(t *testing.T) {
			var externalCalls atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				externalCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer external.Close()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					href := server.URL + "/v7/test-results/104/http-server"
					if linkKind == "results" {
						href = external.URL + "/v7/test-results/104/http-server"
					}
					syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "104", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}, "_links": map[string]any{"testResults": []any{map[string]any{"href": href}}}})
					return
				}
				syntheticJSON(t, w, http.StatusOK, map[string]any{"results": []any{}, "_links": map[string]any{"next": map[string]any{"href": external.URL + "/steal-token"}}})
			}))
			defer server.Close()
			_, _, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
			if err == nil {
				t.Fatal("external API link was accepted")
			}
			if externalCalls.Load() != 0 {
				t.Fatalf("sent %d requests to an external API origin", externalCalls.Load())
			}
		})
	}
}

func TestSyntheticsDoesNotRetryAmbiguousCreation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
	if err == nil || calls.Load() != 1 {
		t.Fatalf("ambiguous creation must fail without resubmission: err=%v calls=%d", err, calls.Load())
	}
}

func TestSyntheticsRetriesExplicitRateLimitRejection(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if creates.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.Header().Set("X-Instant-Test-Rate-Limit-Reset", strconv.FormatInt(time.Now().Unix(), 10))
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "105", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "105"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "errorType": "None"}}})
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
	if err != nil || creates.Load() != 2 {
		t.Fatalf("expected bounded retry of rejected creation: err=%v creates=%d", err, creates.Load())
	}
}

func TestSyntheticsRejectsPassingAgentsFromDifferentRounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "106", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}, map[string]any{"agentId": "7", "agentType": "cloud"}}})
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "106"}, "results": []any{
			map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "errorType": "None"},
			map[string]any{"agent": map[string]any{"agentId": "7"}, "roundId": 1793268060, "responseCode": 200, "errorType": "None"},
		}})
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, `{"version":1,"accountGroupId":"123","timeout":"50ms","pollInterval":"1ms","tests":[{"name":"http","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"},{"agentId":"7"}]}}]}`)
	if err == nil {
		t.Fatal("passing agents from distinct rounds were combined into a passing gate")
	}
}

func TestSyntheticsKnownPacketLossFailsWithoutWaitingForLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "107", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "107"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "loss": 100}}})
	}))
	defer server.Close()
	stdout, _, err := executeSyntheticSuite(t, server.URL, `{"version":1,"accountGroupId":"123","timeout":"100ms","pollInterval":"1ms","tests":[{"name":"network","type":"agent-to-server","config":{"server":"target.invalid:443","protocol":"tcp","agents":[{"agentId":"6"}]},"thresholds":{"maxLoss":0,"maxLatency":100}}]}`, "--json")
	if err == nil {
		t.Fatal("100% packet loss passed")
	}
	var report struct {
		Tests []struct {
			Status string `json:"status"`
		} `json:"tests"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid report: %v\n%s", err, stdout)
	}
	if len(report.Tests) != 1 || report.Tests[0].Status != "failed" {
		t.Fatalf("known loss must fail, not wait for missing latency: %s", stdout)
	}
}

func TestSyntheticsDoesNotFollowAPIRedirects(t *testing.T) {
	var redirected atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer external.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "108", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
			return
		}
		w.Header().Set("Location", external.URL+"/token-target")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, _, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
	if err == nil || redirected.Load() != 0 {
		t.Fatalf("API redirect must be rejected without external requests: error=%v requests=%d", err, redirected.Load())
	}
}

func TestSyntheticsRetriesTransientResultReads(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "109", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud"}}})
			return
		}
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "109"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "errorType": "None"}}})
	}))
	defer server.Close()
	stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
	if err != nil || reads.Load() != 2 {
		t.Fatalf("expected retry of transient result GET: err=%v reads=%d\n%s\n%s", err, reads.Load(), stdout, stderr)
	}
}

func TestSyntheticsRequiresVerifiedExpandedAgentAssignments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		agents any
	}{
		{name: "missing-expansion"},
		{name: "empty-expansion", agents: []any{}},
		{name: "missing-agent-type", agents: []any{map[string]any{"agentId": "6"}}},
		{name: "unknown-agent-type", agents: []any{map[string]any{"agentId": "6", "agentType": "unknown"}}},
		{name: "enterprise-cluster", agents: []any{map[string]any{"agentId": "6", "agentType": "enterprise-cluster"}}},
		{name: "different-assigned-agent", agents: []any{map[string]any{"agentId": "7", "agentType": "cloud"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if r.URL.Query().Get("expand") != "agent" {
						t.Error("instant creation must request expanded agent assignments")
					}
					body := map[string]any{"testId": "110"}
					if tc.agents != nil {
						body["agents"] = tc.agents
					}
					syntheticJSON(t, w, http.StatusCreated, body)
					return
				}
				reads.Add(1)
				syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "110"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "errorType": "None"}}})
			}))
			defer server.Close()
			stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"), "--json")
			if err == nil {
				t.Fatalf("unverified assignment passed the CI gate\n%s\n%s", stdout, stderr)
			}
			if reads.Load() != 0 {
				t.Fatalf("polled results before verifying assignments: %d reads", reads.Load())
			}
			var report struct {
				Tests []struct {
					Status string `json:"status"`
				} `json:"tests"`
			}
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("expected sanitized error report: %v\n%s", err, stdout)
			}
			if len(report.Tests) != 1 || report.Tests[0].Status != "error" {
				t.Fatalf("expected assignment infrastructure error, got %s", stdout)
			}
		})
	}
}

func TestSyntheticsAcceptsEnterpriseAgentAssignment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "111", "agents": []any{map[string]any{"agentId": "6", "agentType": "enterprise"}}})
			return
		}
		syntheticJSON(t, w, http.StatusOK, map[string]any{"test": map[string]any{"testId": "111"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": 200, "errorType": "None"}}})
	}))
	defer server.Close()
	stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"))
	if err != nil {
		t.Fatalf("ordinary Enterprise Agent assignment should pass: %v\n%s\n%s", err, stdout, stderr)
	}
}
