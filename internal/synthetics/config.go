// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	defaultTimeout      = 5 * time.Minute
	defaultPollInterval = 5 * time.Second
)

var numericID = regexp.MustCompile(`^[0-9]+$`)

type Config struct {
	Version        int          `json:"version"`
	AccountGroupID string       `json:"accountGroupId"`
	Timeout        string       `json:"timeout,omitempty"`
	PollInterval   string       `json:"pollInterval,omitempty"`
	Tests          []TestConfig `json:"tests"`
}

type TestConfig struct {
	Name          string                     `json:"name"`
	Type          string                     `json:"type"`
	ExecutionRule string                     `json:"executionRule,omitempty"`
	Config        map[string]json.RawMessage `json:"config"`
	Thresholds    Thresholds                 `json:"thresholds,omitempty"`
}

type Thresholds struct {
	ExpectedResponseCodes []int    `json:"expectedResponseCodes,omitempty"`
	MaxResponseTime       *float64 `json:"maxResponseTime,omitempty"`
	MaxTotalTime          *float64 `json:"maxTotalTime,omitempty"`
	MaxLoss               *float64 `json:"maxLoss,omitempty"`
	MaxLatency            *float64 `json:"maxLatency,omitempty"`
	MaxJitter             *float64 `json:"maxJitter,omitempty"`
}

type Options struct {
	BaseURL        string
	Token          string
	AccountGroupID string
	Timeout        time.Duration
	PollInterval   time.Duration
}

type Report struct {
	Version    int          `json:"version"`
	RunID      string       `json:"runId"`
	StartedAt  time.Time    `json:"startedAt"`
	FinishedAt time.Time    `json:"finishedAt"`
	Tests      []TestResult `json:"tests"`
}

type TestResult struct {
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	ExecutionRule string        `json:"executionRule"`
	TestID        string        `json:"testId,omitempty"`
	Status        string        `json:"status"`
	Message       string        `json:"message,omitempty"`
	Agents        []AgentResult `json:"agents"`
}

type AgentResult struct {
	AgentID string             `json:"agentId"`
	RoundID string             `json:"roundId,omitempty"`
	Status  string             `json:"status"`
	Message string             `json:"message,omitempty"`
	Metrics map[string]float64 `json:"metrics,omitempty"`
	URL     string             `json:"url,omitempty"`
}

func (r Report) Failed() bool {
	for _, result := range r.Tests {
		switch result.Status {
		case "error", "timed-out":
			return true
		case "failed":
			if result.ExecutionRule != "non-blocking" {
				return true
			}
		}
	}
	return false
}

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open synthetic configuration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return Config{}, fmt.Errorf("read synthetic configuration: %w", err)
	}
	if len(data) > 4<<20 {
		return Config{}, fmt.Errorf("synthetic configuration exceeded size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode synthetic configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("synthetic configuration must contain one JSON object")
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("synthetic configuration version must be 1")
	}
	if c.AccountGroupID != "" && !numericID.MatchString(c.AccountGroupID) {
		return fmt.Errorf("accountGroupId must be a numeric string")
	}
	if _, err := parseDuration(c.Timeout, defaultTimeout, "timeout"); err != nil {
		return err
	}
	if _, err := parseDuration(c.PollInterval, defaultPollInterval, "pollInterval"); err != nil {
		return err
	}
	if len(c.Tests) == 0 || len(c.Tests) > 100 {
		return fmt.Errorf("synthetic configuration requires between 1 and 100 tests")
	}
	names := make(map[string]bool)
	for index, test := range c.Tests {
		if err := test.validate(); err != nil {
			return fmt.Errorf("test %d: %w", index+1, err)
		}
		if names[test.Name] {
			return fmt.Errorf("test %d: duplicate test name", index+1)
		}
		names[test.Name] = true
	}
	return nil
}

func (t TestConfig) validate() error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 200 {
		return fmt.Errorf("name must contain between 1 and 200 bytes")
	}
	if t.Type != "http-server" && t.Type != "agent-to-server" {
		return fmt.Errorf("unsupported synthetic test type; supported types are http-server and agent-to-server")
	}
	if t.ExecutionRule != "" && t.ExecutionRule != "blocking" && t.ExecutionRule != "non-blocking" && t.ExecutionRule != "skipped" {
		return fmt.Errorf("executionRule must be blocking, non-blocking, or skipped")
	}
	if t.Config == nil {
		return fmt.Errorf("config must be an instant-test payload object")
	}
	for _, value := range t.Config {
		if !json.Valid(value) {
			return fmt.Errorf("config contains an invalid JSON payload value")
		}
	}
	for _, field := range []string{"testId", "type", "createdDate", "modifiedDate", "savedEvent", "_links"} {
		if _, ok := t.Config[field]; ok {
			return fmt.Errorf("config must not contain read-only field %s", field)
		}
	}
	if _, err := t.agentIDs(); err != nil {
		return err
	}
	if direction, ok := t.Config["direction"]; ok && !bytes.Equal(direction, []byte(`"to-target"`)) {
		return fmt.Errorf("only to-target network direction is supported")
	}
	if t.Type == "http-server" {
		var target string
		if err := json.Unmarshal(t.Config["url"], &target); err != nil {
			return fmt.Errorf("HTTP config requires a URL string")
		}
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("HTTP config requires an absolute http or https URL")
		}
		if t.Thresholds.MaxLoss != nil || t.Thresholds.MaxLatency != nil || t.Thresholds.MaxJitter != nil {
			return fmt.Errorf("network thresholds require an agent-to-server test")
		}
	} else {
		var target string
		if err := json.Unmarshal(t.Config["server"], &target); err != nil || strings.TrimSpace(target) == "" {
			return fmt.Errorf("network config requires a server string")
		}
		if raw, ok := t.Config["protocol"]; ok {
			var protocol string
			if err := json.Unmarshal(raw, &protocol); err != nil || (protocol != "icmp" && protocol != "tcp" && protocol != "udp") {
				return fmt.Errorf("network protocol must be icmp, tcp, or udp")
			}
		}
		if t.Thresholds.MaxResponseTime != nil || t.Thresholds.MaxTotalTime != nil || len(t.Thresholds.ExpectedResponseCodes) > 0 {
			return fmt.Errorf("HTTP thresholds require an http-server test")
		}
	}
	for name, value := range map[string]*float64{"maxResponseTime": t.Thresholds.MaxResponseTime, "maxTotalTime": t.Thresholds.MaxTotalTime, "maxLoss": t.Thresholds.MaxLoss, "maxLatency": t.Thresholds.MaxLatency, "maxJitter": t.Thresholds.MaxJitter} {
		if value != nil && (*value < 0 || math.IsInf(*value, 0) || math.IsNaN(*value)) {
			return fmt.Errorf("%s must be a finite nonnegative number", name)
		}
	}
	if t.Thresholds.MaxLoss != nil && *t.Thresholds.MaxLoss > 100 {
		return fmt.Errorf("maxLoss must not exceed 100 percent")
	}
	seenCodes := make(map[int]bool)
	for _, code := range t.Thresholds.ExpectedResponseCodes {
		if code < 100 || code > 599 || seenCodes[code] {
			return fmt.Errorf("expectedResponseCodes requires distinct valid HTTP status codes")
		}
		seenCodes[code] = true
	}
	return nil
}

func (t TestConfig) agentIDs() ([]string, error) {
	var agents []struct {
		AgentID   string `json:"agentId"`
		AgentType string `json:"agentType"`
	}
	if err := json.Unmarshal(t.Config["agents"], &agents); err != nil || len(agents) == 0 || len(agents) > 100 {
		return nil, fmt.Errorf("config requires between 1 and 100 agents")
	}
	ids := make([]string, 0, len(agents))
	seen := make(map[string]bool)
	for _, agent := range agents {
		if !numericID.MatchString(agent.AgentID) || seen[agent.AgentID] {
			return nil, fmt.Errorf("config requires distinct numeric string agent IDs")
		}
		if agent.AgentType == "enterprise-cluster" {
			return nil, fmt.Errorf("cluster agent result completeness is not supported")
		}
		seen[agent.AgentID] = true
		ids = append(ids, agent.AgentID)
	}
	return ids, nil
}

func parseDuration(value string, fallback time.Duration, field string) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", field)
	}
	return duration, nil
}
