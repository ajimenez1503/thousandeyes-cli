// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResultRetainsAgentName(t *testing.T) {
	var row resultRow
	if err := json.Unmarshal([]byte(`{"agent":{"agentId":"6","agentName":"Example agent"},"roundId":10,"responseCode":200}`), &row); err != nil {
		t.Fatal(err)
	}
	result, ready, err := evaluateRow(TestConfig{Type: "http-server"}, row)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	data, _ := json.Marshal(result)
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["agentName"] != "Example agent" {
		t.Fatalf("agent name was dropped: %s", data)
	}
}

func TestReportTargetsExcludeSecretsAndInvalidNetworkValues(t *testing.T) {
	for _, item := range []struct{ kind, payload, want string }{
		{"http-server", `{"url":"https://user:password@example.invalid/health?access_token=secret#fragment"}`, "https://example.invalid/health"},
		{"http-server", `{"url":"https://example.invalid/a%73ecret/health?token=hidden"}`, "https://example.invalid/a%5BREDACTED%5D/health"},
		{"http-server", `{"url":"https://example.invalid/health%20"}`, "https://example.invalid/health%20"},
		{"http-server", `{"url":"https://example.invalid/files/a%2Fb%3Bc"}`, "https://example.invalid/files/a%2Fb%3Bc"},
		{"http-server", `{"url":"javascript:secret"}`, ""},
		{"http-server", `{"url":"https://example.invalid/` + strings.Repeat("x", 2100) + `"}`, ""},
		{"agent-to-server", `{"server":"2001:db8::1","port":443}`, "[2001:db8::1]:443"},
		{"agent-to-server", `{"server":"[2001:db8::1]","port":443}`, "[2001:db8::1]:443"},
		{"agent-to-server", `{"server":"example.invalid","port":443}`, "example.invalid:443"},
		{"agent-to-server", `{"server":"example.invalid","port":"secret"}`, "example.invalid"},
		{"agent-to-server", `{"server":"user:secret@example.invalid","port":443}`, ""},
		{"agent-to-server", `{"server":"example.invalid?token=secret"}`, ""},
		{"agent-to-server", `{"server":"[example.invalid"}`, ""},
	} {
		t.Run(item.kind+item.want, func(t *testing.T) {
			var config map[string]json.RawMessage
			if err := json.Unmarshal([]byte(item.payload), &config); err != nil {
				t.Fatal(err)
			}
			if got := reportTarget(TestConfig{Type: item.kind, Config: config}, "secret"); got != item.want {
				t.Fatalf("target=%q want %q", got, item.want)
			}
		})
	}
}

func TestReportNamesAreBoundedAndSafeForTerminalOutput(t *testing.T) {
	got := reportText("\nAgent secret\x1b[31m\r"+strings.Repeat("界", 200), "secret", 160)
	if len([]rune(got)) > 160 || strings.ContainsAny(got, "\n\r\x1b") || strings.Contains(got, "secret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("unsafe report name: %q", got)
	}
}

func TestMissingAppLinksUseValidatedPublicIdentityOnly(t *testing.T) {
	for _, base := range []string{"https://api.thousandeyes.com", "https://api.thousandeyes.com/v7", "https://API.THOUSANDEYES.COM"} {
		client, err := newAPIClient(Options{BaseURL: base, Token: "secret", AccountGroupID: "123"}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"http-server", "agent-to-server"} {
			agents := []AgentResult{{AgentID: "6", RoundID: "10"}}
			client.enrichAgents(agents, nil, "101")
			if got, want := agents[0].URL, "https://app.thousandeyes.com/view/tests?__a=123&agentId=6&roundId=10&testId=101"; got != want {
				t.Fatalf("%s fallback=%q want %q", kind, got, want)
			}
		}
		for _, round := range []string{"", "0", "-1", "10&secret=1", strings.Repeat("1", 33)} {
			if got := client.appLink("101", "6", round); got != "" {
				t.Fatalf("link generated for invalid round %q", round)
			}
		}
		if got := client.appLink("101/secret", "6", "10"); got != "" {
			t.Fatal("link generated for invalid test identity")
		}
		approved := "https://app.thousandeyes.eu/view/tests?testId=101"
		agents := []AgentResult{{AgentID: "6", RoundID: "10", URL: approved}}
		client.enrichAgents(agents, nil, "101")
		if agents[0].URL != approved {
			t.Fatal("fallback replaced an API-provided approved link")
		}
	}
	for _, base := range []string{"https://api.custom.invalid", "http://api.thousandeyes.com", "https://api.thousandeyes.com:443", "https://api.thousandeyes.com/custom", "http://localhost:8080"} {
		client, err := newAPIClient(Options{BaseURL: base, Token: "secret", AccountGroupID: "123"}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if got := client.appLink("101", "6", "10"); got != "" {
			t.Fatalf("unrecognized API origin produced a link: %s", base)
		}
	}
}
