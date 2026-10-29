// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestJUnitReportsAgentFailuresErrorsAndSkippedTests(t *testing.T) {
	report := Report{
		Version: 1, RunID: "run-1", StartedAt: time.Unix(100, 0), FinishedAt: time.Unix(102, 0),
		Tests: []TestResult{
			{Name: "HTTP <canary>", Type: "http-server", ExecutionRule: "blocking", Status: "passed", Agents: []AgentResult{{AgentID: "6", Status: "passed", RoundID: "100", Metrics: map[string]float64{"responseCode": 200}}}},
			{Name: "network", Type: "agent-to-server", ExecutionRule: "non-blocking", Status: "failed", Agents: []AgentResult{{AgentID: "6", Status: "failed", Message: "loss exceeds threshold"}}},
			{Name: "missing", Type: "http-server", ExecutionRule: "blocking", Status: "timed-out", Message: "results incomplete"},
			{Name: "disabled", Type: "http-server", ExecutionRule: "skipped", Status: "skipped"},
		},
	}
	var out bytes.Buffer
	if err := WriteJUnit(&out, report); err != nil {
		t.Fatal(err)
	}
	var suite junitSuite
	if err := xml.Unmarshal(out.Bytes(), &suite); err != nil {
		t.Fatalf("invalid JUnit XML: %v", err)
	}
	if suite.Tests != 4 || suite.Failures != 1 || suite.Errors != 1 || suite.Skipped != 1 {
		t.Fatalf("incorrect JUnit totals: %+v", suite)
	}
	if suite.Cases[0].Name != "HTTP <canary> / agent 6" || !strings.Contains(suite.Cases[0].SystemOut, `"responseCode":200`) {
		t.Fatalf("lost case identity or metrics: %+v", suite.Cases[0])
	}
	if suite.Cases[1].Failure.Type != "non-blocking" || suite.Cases[2].Error.Type != "timed-out" {
		t.Fatal("JUnit did not preserve assertion policy and execution-error distinction")
	}
}

type brokenReportWriter struct{}

func TestJUnitPreservesExecutionErrorAfterPassingAgentRows(t *testing.T) {
	report := Report{Tests: []TestResult{{
		Name: "incomplete", Type: "http-server", Status: "timed-out", Message: "results incomplete",
		Agents: []AgentResult{{AgentID: "6", Status: "passed"}},
	}}}
	var out bytes.Buffer
	if err := WriteJUnit(&out, report); err != nil {
		t.Fatal(err)
	}
	var suite junitSuite
	if err := xml.Unmarshal(out.Bytes(), &suite); err != nil {
		t.Fatal(err)
	}
	if suite.Errors != 1 || suite.Tests != 2 {
		t.Fatalf("execution failure disappeared from JUnit: %s", out.String())
	}
}

func (brokenReportWriter) Write([]byte) (int, error) { return 0, errors.New("report sink unavailable") }

func TestReportsPropagateOutputErrors(t *testing.T) {
	for name, write := range map[string]func() error{
		"JSON":    func() error { return WriteJSON(brokenReportWriter{}, Report{}) },
		"JUnit":   func() error { return WriteJUnit(brokenReportWriter{}, Report{}) },
		"summary": func() error { return WriteSummary(brokenReportWriter{}, Report{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(); err == nil {
				t.Fatal("report failure was ignored")
			}
		})
	}
}
