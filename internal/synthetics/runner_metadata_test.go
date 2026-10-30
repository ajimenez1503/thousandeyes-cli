// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type metadataRoundTripper func(*http.Request) (*http.Response, error)

func (transport metadataRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestRunnerFillsMissingAppLinksForEachAdapter(t *testing.T) {
	for _, item := range []struct {
		kind, layer, payload string
		metrics              map[string]float64
	}{
		{
			kind: "http-server", layer: "http-server",
			payload: `{"url":"https://service.invalid/health","agents":[{"agentId":"6"},{"agentId":"7"}]}`,
			metrics: map[string]float64{"responseCode": 200, "responseTime": 12, "totalTime": 20},
		},
		{
			kind: "agent-to-server", layer: "network",
			payload: `{"server":"service.invalid","port":443,"protocol":"tcp","agents":[{"agentId":"6"},{"agentId":"7"}]}`,
			metrics: map[string]float64{"loss": 0, "avgLatency": 5, "jitter": 1},
		},
	} {
		t.Run(item.kind, func(t *testing.T) {
			client, err := newAPIClient(Options{
				BaseURL: "https://api.thousandeyes.com", Token: "runner-metadata-fake-token", AccountGroupID: "123",
			}, time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]json.RawMessage
			if err := json.Unmarshal([]byte(item.payload), &config); err != nil {
				t.Fatal(err)
			}
			creates, polls := 0, 0
			// Keep the public origin for fallback selection, but intercept every
			// request so this runner test never contacts the live API.
			client.http.Transport = metadataRoundTripper(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme != "https" || request.URL.Host != "api.thousandeyes.com" || request.URL.Query().Get("aid") != "123" {
					t.Fatalf("unexpected request origin or account: %s", request.URL)
				}
				if request.Header.Get("Authorization") != "Bearer runner-metadata-fake-token" {
					t.Fatal("request lost bearer authentication")
				}
				var body any
				status := http.StatusOK
				switch {
				case request.Method == http.MethodPost && request.URL.Path == "/v7/tests/"+item.kind+"/instant":
					creates++
					if request.URL.Query().Get("expand") != "agent" {
						t.Fatal("creation must request expanded agents for the name fallback")
					}
					var got, want map[string]any
					if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(item.payload), &want); err != nil {
						t.Fatal(err)
					}
					want["testName"] = "ci-metadata-1"
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("native creation payload=%v want %v", got, want)
					}
					status = http.StatusCreated
					body = map[string]any{
						"testId": "101", "type": item.kind,
						"agents": []any{
							map[string]any{"agentId": "6", "agentType": "cloud", "agentName": "Creation six"},
							map[string]any{"agentId": "7", "agentType": "enterprise", "agentName": "Creation seven"},
						},
						"_links": map[string]any{"testResults": []any{map[string]any{"href": "/v7/test-results/101/" + item.layer}}},
					}
				case request.Method == http.MethodGet && request.URL.Path == "/v7/test-results/101/"+item.layer:
					polls++
					rows := []any{}
					for _, id := range []string{"6", "7"} {
						if polls == 1 && id == "7" {
							continue
						}
						agent := map[string]any{"agentId": id}
						if id == "6" {
							agent["agentName"] = "Result six"
						}
						row := map[string]any{"agent": agent, "roundId": 1700000000}
						for metric, value := range item.metrics {
							row[metric] = value
						}
						// Neither adapter receives an appLink in its result rows.
						rows = append(rows, row)
					}
					body = map[string]any{"test": map[string]any{"testId": "101"}, "results": rows}
				default:
					t.Fatalf("unexpected API call: %s %s", request.Method, request.URL)
				}
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{
					StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Request: request,
					Header: http.Header{"Content-Type": {"application/hal+json"}},
				}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := TestResult{Agents: []AgentResult{{AgentID: "6", Status: "pending"}, {AgentID: "7", Status: "pending"}}}
			if err := runTest(ctx, client, TestConfig{Type: item.kind, Config: config}, []string{"6", "7"}, "metadata", 0, &result); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || polls != 2 || result.TestID != "101" || result.Status != "passed" || len(result.Agents) != 2 {
				t.Fatalf("creation/polling/result: creates=%d polls=%d result=%+v", creates, polls, result)
			}
			for index, agent := range result.Agents {
				id, name := "6", "Result six"
				if index == 1 {
					id, name = "7", "Creation seven"
				}
				if agent.AgentID != id || agent.AgentName != name || agent.RoundID != "1700000000" || agent.Status != "passed" || !reflect.DeepEqual(agent.Metrics, item.metrics) {
					t.Errorf("agent metadata or measurements were lost: %+v", agent)
				}
				link, err := url.Parse(agent.URL)
				if err != nil || link.Scheme != "https" || link.Host != "app.thousandeyes.com" || link.Path != "/view/tests" {
					t.Fatalf("missing or invalid fallback result link: %q", agent.URL)
				}
				if want := (url.Values{"__a": {"123"}, "testId": {"101"}, "agentId": {id}, "roundId": {"1700000000"}}); !reflect.DeepEqual(link.Query(), want) {
					t.Errorf("fallback link identities=%v want %v", link.Query(), want)
				}
			}
		})
	}
}
