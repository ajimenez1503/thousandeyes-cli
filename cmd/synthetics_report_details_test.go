// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type syntheticDetailsReport struct {
	Version int `json:"version"`
	Tests   []struct {
		Target string `json:"target"`
		Status string `json:"status"`
		Agents []struct {
			AgentID   string `json:"agentId"`
			AgentName string `json:"agentName"`
			Status    string `json:"status"`
			URL       string `json:"url"`
		} `json:"agents"`
	} `json:"tests"`
}

func readSyntheticDetailsReport(t *testing.T, path string) (syntheticDetailsReport, []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report syntheticDetailsReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report, data
}

func TestSyntheticsReportDetailsHTTPMetadataAcrossOutputs(t *testing.T) {
	const rawTarget = "https://user-secret:password-secret@target.invalid/health?token=query-secret#fragment-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v7/tests/http-server/instant":
			if r.URL.Query().Get("expand") != "agent" {
				t.Error("creation must retain expanded agent metadata")
			}
			var request struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.URL != rawTarget {
				t.Error("report sanitization changed the native test target")
			}
			syntheticJSON(t, w, http.StatusCreated, map[string]any{
				"testId": "101", "agents": []any{
					map[string]any{"agentId": "6", "agentType": "cloud", "agentName": "Creation six"},
					map[string]any{"agentId": "7", "agentType": "enterprise", "agentName": "Creation seven"},
					map[string]any{"agentId": "8", "agentType": "cloud"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v7/test-results/101/http-server":
			syntheticJSON(t, w, http.StatusOK, map[string]any{"results": []any{
				map[string]any{"agent": map[string]any{"agentId": "6", "agentName": "Result \x00Agent synthetics-test-token"}, "roundId": 1793268000, "responseCode": 200, "headers": "secret-response-header"},
				map[string]any{"agent": map[string]any{"agentId": "7"}, "roundId": 1793268000, "responseCode": 200},
				map[string]any{"agent": map[string]any{"agentId": "8"}, "roundId": 1793268000, "responseCode": 200},
			}})
		default:
			t.Errorf("metadata must not add API requests: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	reportPath, junitPath := filepath.Join(directory, "result.json"), filepath.Join(directory, "result.xml")
	payload := `{"version":1,"accountGroupId":"123","timeout":"1s","pollInterval":"1ms","tests":[{"name":"homepage","type":"http-server","config":{"url":"` + rawTarget + `","agents":[{"agentId":"6"},{"agentId":"7"},{"agentId":"8"}]}}]}`
	stdout, stderr, err := executeSyntheticSuite(t, server.URL, payload, "--json-report", reportPath, "--junit-report", junitPath)
	if err != nil {
		t.Fatalf("passing suite failed: %v\n%s\n%s", err, stdout, stderr)
	}
	report, data := readSyntheticDetailsReport(t, reportPath)
	if report.Version != 1 || len(report.Tests) != 1 || len(report.Tests[0].Agents) != 3 {
		t.Fatalf("unexpected report: %s", data)
	}
	result := report.Tests[0]
	if result.Target != "https://target.invalid/health" {
		t.Errorf("target = %q", result.Target)
	}
	for index, wantName := range []string{"Result Agent [REDACTED]", "Creation seven", ""} {
		if got := result.Agents[index].AgentName; got != wantName {
			t.Errorf("agent %d name = %q, want %q", index, got, wantName)
		}
		if got := result.Agents[index].URL; got != "" {
			t.Errorf("custom API origin generated a public app URL: %q", got)
		}
		if result.Agents[index].Status != "passed" {
			t.Errorf("agent status changed: %+v", result.Agents[index])
		}
	}
	for _, want := range []string{"https://target.invalid/health", "Result Agent [REDACTED]", "Creation seven"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("terminal summary omitted %q: %s", want, stdout)
		}
	}
	xmlBytes, err := os.ReadFile(junitPath)
	if err != nil {
		t.Fatal(err)
	}
	var suite struct {
		Cases []struct {
			Properties []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"properties>property"`
		} `xml:"testcase"`
	}
	if err := xml.Unmarshal(xmlBytes, &suite); err != nil {
		t.Fatal(err)
	}
	if len(suite.Cases) != 3 {
		t.Fatalf("JUnit lost per-agent cases: %s", xmlBytes)
	}
	for index, testCase := range suite.Cases {
		properties := map[string]string{}
		for _, property := range testCase.Properties {
			properties[property.Name] = property.Value
		}
		if properties["target"] != "https://target.invalid/health" {
			t.Errorf("JUnit case %d missing sanitized target", index)
		}
		if properties["agentName"] != result.Agents[index].AgentName {
			t.Errorf("JUnit case %d agent name differs from JSON", index)
		}
	}
	combined := string(data) + string(xmlBytes) + stdout + stderr
	for _, secret := range []string{"user-secret", "password-secret", "query-secret", "fragment-secret", "synthetics-test-token", "secret-response-header", "\x00"} {
		if strings.Contains(combined, secret) {
			t.Errorf("output exposed %q", secret)
		}
	}
	var raw struct {
		Tests []struct {
			Agents []map[string]any `json:"agents"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw.Tests[0].Agents[2]["agentName"]; present {
		t.Error("missing agent name should be omitted, not fabricated")
	}
}

func TestSyntheticsReportDetailsRedactsControlSeparatedTokenAcrossOutputs(t *testing.T) {
	for _, source := range []string{"result-name", "creation-fallback"} {
		t.Run(source, func(t *testing.T) {
			const unsafeName = "Agent synthetics-test-\x00token"
			const wantName = "Agent [REDACTED]"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v7/tests/http-server/instant":
					name := "Creation agent"
					if source == "creation-fallback" {
						name = unsafeName
					}
					syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "101", "agents": []any{
						map[string]any{"agentId": "6", "agentType": "cloud", "agentName": name},
					}})
				case r.Method == http.MethodGet && r.URL.Path == "/v7/test-results/101/http-server":
					agent := map[string]any{"agentId": "6"}
					if source == "result-name" {
						agent["agentName"] = unsafeName
					}
					syntheticJSON(t, w, http.StatusOK, map[string]any{"results": []any{
						map[string]any{"agent": agent, "roundId": 1793268000, "responseCode": 200},
					}})
				default:
					t.Errorf("metadata must not add API requests: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			directory := t.TempDir()
			reportPath, junitPath := filepath.Join(directory, "result.json"), filepath.Join(directory, "result.xml")
			stdout, stderr, err := executeSyntheticSuite(t, server.URL, singleHTTPConfig("blocking"), "--json-report", reportPath, "--junit-report", junitPath)
			if err != nil {
				t.Fatalf("metadata sanitization changed passing outcome: %v", err)
			}
			report, jsonBytes := readSyntheticDetailsReport(t, reportPath)
			if len(report.Tests) != 1 || len(report.Tests[0].Agents) != 1 || report.Tests[0].Status != "passed" || report.Tests[0].Agents[0].Status != "passed" {
				t.Fatalf("metadata sanitization changed agent results: %s", jsonBytes)
			}
			if got := report.Tests[0].Agents[0].AgentName; got != wantName {
				t.Errorf("JSON agent name = %q, want %q", got, wantName)
			}
			xmlBytes, err := os.ReadFile(junitPath)
			if err != nil {
				t.Fatal(err)
			}
			var suite struct {
				Errors   int `xml:"errors,attr"`
				Failures int `xml:"failures,attr"`
				Cases    []struct {
					Properties []struct {
						Name  string `xml:"name,attr"`
						Value string `xml:"value,attr"`
					} `xml:"properties>property"`
				} `xml:"testcase"`
			}
			if err := xml.Unmarshal(xmlBytes, &suite); err != nil {
				t.Fatal(err)
			}
			if suite.Errors != 0 || suite.Failures != 0 || len(suite.Cases) != 1 {
				t.Fatalf("JUnit outcome changed: %s", xmlBytes)
			}
			properties := map[string]string{}
			for _, property := range suite.Cases[0].Properties {
				properties[property.Name] = property.Value
			}
			if properties["agentName"] != wantName {
				t.Errorf("JUnit agent name = %q, want %q", properties["agentName"], wantName)
			}
			for channel, output := range map[string]string{"JSON": string(jsonBytes), "terminal": stdout, "JUnit": string(xmlBytes)} {
				if strings.Contains(output, "synthetics-test-token") || strings.ContainsRune(output, '\x00') {
					t.Errorf("%s exposed token or control character", channel)
				}
				if !strings.Contains(output, wantName) {
					t.Errorf("%s omitted the redacted name", channel)
				}
			}
			if strings.Contains(stderr, "synthetics-test-token") || strings.ContainsRune(stderr, '\x00') {
				t.Error("stderr exposed token or control character")
			}
		})
	}
}

func TestSyntheticsReportDetailsRetainedOnPartialTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v7/tests/http-server/instant":
			syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "101", "agents": []any{
				map[string]any{"agentId": "6", "agentType": "cloud", "agentName": "Creation six"},
				map[string]any{"agentId": "7", "agentType": "enterprise", "agentName": "Creation seven"},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v7/test-results/101/http-server":
			syntheticJSON(t, w, http.StatusOK, map[string]any{"results": []any{
				map[string]any{"agent": map[string]any{"agentId": "6", "agentName": "Result six"}, "roundId": 1793268000, "responseCode": 200},
			}})
		default:
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reportPath := filepath.Join(t.TempDir(), "result.json")
	payload := `{"version":1,"accountGroupId":"123","timeout":"200ms","pollInterval":"1ms","tests":[{"name":"partial","type":"http-server","config":{"url":"https://target.invalid/health?secret=hidden-query","agents":[{"agentId":"6"},{"agentId":"7"}]}},{"name":"unstarted","type":"http-server","config":{"url":"https://later.invalid/ready#hidden-fragment","agents":[{"agentId":"6"}]}}]}`
	_, _, err := executeSyntheticSuite(t, server.URL, payload, "--json-report", reportPath)
	if err == nil {
		t.Fatal("missing required agent must remain a blocking timeout")
	}
	report, data := readSyntheticDetailsReport(t, reportPath)
	if len(report.Tests) != 2 || len(report.Tests[0].Agents) != 2 {
		t.Fatalf("unexpected partial report: %s", data)
	}
	partial := report.Tests[0]
	if partial.Status != "timed-out" || partial.Target != "https://target.invalid/health" {
		t.Errorf("partial test metadata/status: %+v", partial)
	}
	if partial.Agents[0].Status != "passed" || partial.Agents[0].AgentName != "Result six" {
		t.Errorf("completed agent lost metadata: %+v", partial.Agents[0])
	}
	if partial.Agents[1].Status != "timed-out" || partial.Agents[1].AgentName != "Creation seven" {
		t.Errorf("missing agent lost creation name: %+v", partial.Agents[1])
	}
	if report.Tests[1].Status != "skipped" || report.Tests[1].Target != "https://later.invalid/ready" {
		t.Errorf("unstarted test lost configured target: %+v", report.Tests[1])
	}
	for _, secret := range []string{"hidden-query", "hidden-fragment"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("partial report exposed %q", secret)
		}
	}
}

func TestSyntheticsReportDetailsNetworkTarget(t *testing.T) {
	for _, testCase := range []struct {
		name, server, want string
		port               int
	}{
		{name: "IPv6", server: "2001:db8::10", port: 443, want: "[2001:db8::10]:443"},
		{name: "hostname", server: "target.invalid", port: 443, want: "target.invalid:443"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v7/tests/agent-to-server/instant":
					syntheticJSON(t, w, http.StatusCreated, map[string]any{"testId": "102", "agents": []any{map[string]any{"agentId": "6", "agentType": "cloud", "agentName": "Network agent"}}})
				case r.Method == http.MethodGet && r.URL.Path == "/v7/test-results/102/network":
					syntheticJSON(t, w, http.StatusOK, map[string]any{"results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "loss": 0, "avgLatency": 1, "jitter": 0}}})
				default:
					t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			payload, err := json.Marshal(map[string]any{"version": 1, "accountGroupId": "123", "tests": []any{map[string]any{"name": "network", "type": "agent-to-server", "config": map[string]any{"server": testCase.server, "port": testCase.port, "protocol": "tcp", "agents": []any{map[string]any{"agentId": "6"}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			reportPath := filepath.Join(t.TempDir(), "result.json")
			stdout, _, err := executeSyntheticSuite(t, server.URL, string(payload), "--json-report", reportPath)
			if err != nil {
				t.Fatal(err)
			}
			report, data := readSyntheticDetailsReport(t, reportPath)
			if len(report.Tests) != 1 || report.Tests[0].Target != testCase.want {
				t.Fatalf("network target missing or ambiguous: %s", data)
			}
			if !strings.Contains(stdout, testCase.want) || !strings.Contains(stdout, "Network agent") {
				t.Errorf("terminal summary omitted metadata: %s", stdout)
			}
		})
	}
}
