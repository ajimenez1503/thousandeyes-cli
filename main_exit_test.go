// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableReturnsNonzeroOnCommandAndAuthenticationErrors(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "thousandeyes")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"unknown-command"}, {"tests", "list"}, {"synthetics", "run-tests", "--config", "nonexistent.json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := exec.Command(binary, args...)
			command.Env = syntheticProcessEnv("", "")
			out, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("expected nonzero exit for %v; output:\n%s", args, out)
			}
		})
	}
	for _, tc := range []struct {
		name      string
		response  int
		apiError  bool
		wantError bool
	}{
		{name: "passing-measurement", response: 200},
		{name: "failing-measurement", response: 503, wantError: true},
		{name: "creation-authentication-error", apiError: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer process-test-token" {
					t.Error("missing process bearer authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.apiError {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"detail":"private-response-body"}`))
					return
				}
				if r.Method == http.MethodPost {
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"testId":"201","agents":[{"agentId":"6","agentType":"cloud"}]}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"test": map[string]any{"testId": "201"}, "results": []any{map[string]any{"agent": map[string]any{"agentId": "6"}, "roundId": 1793268000, "responseCode": tc.response, "errorType": "None"}}})
			}))
			defer server.Close()
			config := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(config, []byte(`{"version":1,"accountGroupId":"123","timeout":"1s","pollInterval":"1ms","tests":[{"name":"process-http","type":"http-server","config":{"url":"https://target.invalid","agents":[{"agentId":"6"}]}}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			reportPath := filepath.Join(t.TempDir(), "report.json")
			command := exec.Command(binary, "synthetics", "run-tests", "--config", config, "--json-report", reportPath)
			command.Env = syntheticProcessEnv("process-test-token", server.URL)
			out, err := command.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("process error=%v wantError=%v\n%s", err, tc.wantError, out)
			}
			if _, err := os.Stat(reportPath); err != nil {
				t.Fatalf("report missing after execution: %v\n%s", err, out)
			}
			if strings.Contains(string(out), "private-response-body") {
				t.Fatalf("process output exposed raw API body: %s", out)
			}
		})
	}
}

func syntheticProcessEnv(token, baseURL string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "TE_TOKEN=") || strings.HasPrefix(value, "TE_BASE_URL=") {
			continue
		}
		env = append(env, value)
	}
	return append(env, "TE_TOKEN="+token, "TE_BASE_URL="+baseURL)
}
