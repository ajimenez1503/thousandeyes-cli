// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type instantResponse struct {
	TestID string `json:"testId"`
	Type   string `json:"type"`
	Agents []struct {
		AgentID   string `json:"agentId"`
		AgentType string `json:"agentType"`
	} `json:"agents"`
	Links struct {
		TestResults []link `json:"testResults"`
	} `json:"_links"`
}

func Run(ctx context.Context, config Config, options Options) (report Report, err error) {
	report = Report{Version: 1, StartedAt: time.Now().UTC(), Tests: make([]TestResult, 0, len(config.Tests))}
	defer func() { report.FinishedAt = time.Now().UTC() }()
	if err := config.validate(); err != nil {
		return report, err
	}
	timeout, _ := parseDuration(config.Timeout, defaultTimeout, "timeout")
	pollInterval, _ := parseDuration(config.PollInterval, defaultPollInterval, "pollInterval")
	if options.Timeout < 0 || options.PollInterval < 0 {
		return report, fmt.Errorf("timeout and poll interval overrides must be positive")
	}
	if options.Timeout > 0 {
		timeout = options.Timeout
	}
	if options.PollInterval > 0 {
		pollInterval = options.PollInterval
	}
	if options.AccountGroupID == "" {
		options.AccountGroupID = config.AccountGroupID
	}
	if !numericID.MatchString(options.AccountGroupID) {
		return report, fmt.Errorf("an explicit numeric accountGroupId is required")
	}
	if options.BaseURL == "" {
		options.BaseURL = "https://api.thousandeyes.com"
	}
	client, err := newAPIClient(options, pollInterval)
	if err != nil {
		return report, err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return report, fmt.Errorf("generate synthetic run identity")
	}
	report.RunID = hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for index, test := range config.Tests {
		rule := test.ExecutionRule
		if rule == "" {
			rule = "blocking"
		}
		result := TestResult{Name: test.Name, Type: test.Type, ExecutionRule: rule, Status: "skipped", Agents: []AgentResult{}}
		if rule == "skipped" {
			report.Tests = append(report.Tests, result)
			continue
		}
		if err := ctx.Err(); err != nil {
			markExecutionError(&result, err)
			report.Tests = append(report.Tests, result)
			appendUnstarted(&report, config.Tests[index+1:])
			return report, safeExecutionError(err)
		}
		ids, _ := test.agentIDs()
		for _, id := range ids {
			result.Agents = append(result.Agents, AgentResult{AgentID: id, Status: "pending"})
		}
		err = runTest(ctx, client, test, ids, report.RunID, index, &result)
		if err != nil {
			markExecutionError(&result, err)
		}
		report.Tests = append(report.Tests, result)
		if err != nil {
			appendUnstarted(&report, config.Tests[index+1:])
			return report, safeExecutionError(err)
		}
	}
	return report, nil
}

func appendUnstarted(report *Report, tests []TestConfig) {
	for _, test := range tests {
		rule := test.ExecutionRule
		if rule == "" {
			rule = "blocking"
		}
		report.Tests = append(report.Tests, TestResult{Name: test.Name, Type: test.Type, ExecutionRule: rule, Status: "skipped", Message: "not started after an execution error", Agents: []AgentResult{}})
	}
}

func markExecutionError(result *TestResult, err error) {
	status, message := "error", err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		status, message = "timed-out", "execution deadline exceeded before all required measurements arrived"
	}
	if errors.Is(err, context.Canceled) {
		message = "execution canceled"
	}
	result.Status, result.Message = status, message
	for index := range result.Agents {
		if result.Agents[index].Status == "pending" {
			result.Agents[index].Status, result.Agents[index].Message = status, message
		}
	}
}

func safeExecutionError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("synthetic execution deadline exceeded: %w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("synthetic execution canceled: %w", context.Canceled)
	}
	return err
}

func runTest(ctx context.Context, client *apiClient, test TestConfig, ids []string, runID string, index int, result *TestResult) error {
	payload := make(map[string]json.RawMessage, len(test.Config)+1)
	for key, value := range test.Config {
		payload[key] = value
	}
	name, _ := json.Marshal(fmt.Sprintf("ci-%s-%d", runID, index+1))
	payload["testName"] = name
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode instant-test configuration")
	}
	var instant instantResponse
	if err := client.do(ctx, http.MethodPost, client.endpoint("/tests/"+test.Type+"/instant"), body, &instant); err != nil {
		return err
	}
	if !numericID.MatchString(instant.TestID) {
		return fmt.Errorf("instant-test creation returned an invalid test identity")
	}
	result.TestID = instant.TestID
	if instant.Type != "" && instant.Type != test.Type {
		return fmt.Errorf("instant-test creation returned a different test type")
	}
	if err := validateAssignedAgents(instant, ids); err != nil {
		return err
	}
	endpoint, expectedPath, err := instantResultEndpoint(client, instant, test.Type)
	if err != nil {
		return err
	}
	for {
		rows, err := client.resultRows(ctx, endpoint, instant.TestID, expectedPath)
		if err != nil && !pendingResultError(err) {
			return err
		}
		if err == nil {
			outcomes, complete, err := evaluateRows(test, ids, rows)
			if err != nil {
				return err
			}
			result.Agents = outcomes
			if complete {
				result.Status = "passed"
				for _, agent := range outcomes {
					if agent.Status == "failed" {
						result.Status, result.Message = "failed", "one or more agents failed the configured checks"
					}
				}
				return nil
			}
		}
		if err := waitContext(ctx, client.pollInterval); err != nil {
			return err
		}
	}
}

func validateAssignedAgents(instant instantResponse, ids []string) error {
	if len(instant.Agents) == 0 {
		return fmt.Errorf("instant-test creation did not return an expanded agent assignment")
	}
	assigned := make(map[string]bool)
	for _, agent := range instant.Agents {
		if agent.AgentType == "enterprise-cluster" {
			return fmt.Errorf("cluster agent result completeness is not supported")
		}
		if agent.AgentType != "cloud" && agent.AgentType != "enterprise" {
			return fmt.Errorf("instant-test creation returned an unsupported or missing agent type")
		}
		if !numericID.MatchString(agent.AgentID) || assigned[agent.AgentID] {
			return fmt.Errorf("instant-test creation returned invalid agent identities")
		}
		assigned[agent.AgentID] = true
	}
	if len(assigned) != len(ids) {
		return fmt.Errorf("instant-test creation changed the configured agent assignment")
	}
	for _, id := range ids {
		if !assigned[id] {
			return fmt.Errorf("instant-test creation changed the configured agent assignment")
		}
	}
	return nil
}

func instantResultEndpoint(client *apiClient, instant instantResponse, testType string) (string, string, error) {
	layer := testType
	if testType == "agent-to-server" {
		layer = "network"
	}
	endpoint := client.endpoint("/test-results/" + instant.TestID + "/" + layer)
	parsed, _ := url.Parse(endpoint)
	expectedPath := parsed.Path
	found := len(instant.Links.TestResults) == 0
	for _, item := range instant.Links.TestResults {
		u, err := url.Parse(item.Href)
		if err != nil {
			return "", "", fmt.Errorf("invalid instant-test result link")
		}
		u = client.base.ResolveReference(u)
		if !strings.HasPrefix(u.Path, strings.TrimSuffix(expectedPath, layer)) {
			return "", "", fmt.Errorf("instant-test result link identifies a different test endpoint")
		}
		if _, err := client.resultURL(item.Href, u.Path); err != nil {
			return "", "", err
		}
		if u.Path == expectedPath {
			endpoint, found = item.Href, true
		}
	}
	if !found {
		return "", "", fmt.Errorf("instant-test creation returned no primary result endpoint")
	}
	return endpoint, expectedPath, nil
}
