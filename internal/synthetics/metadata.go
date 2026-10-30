// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var reportHostname = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?\.?$`)

func reportText(value, token string, limit int) string {
	// Normalize first so removing controls cannot reconstruct a redacted token.
	value = strings.TrimSpace(strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value))
	if token != "" {
		value = strings.ReplaceAll(value, token, "[REDACTED]")
	}
	characters := []rune(value)
	if len(characters) > limit {
		value = string(characters[:limit-3]) + "..."
	}
	return value
}

func reportTarget(test TestConfig, token string) string {
	var target string
	if test.Type == "http-server" {
		if err := json.Unmarshal(test.Config["url"], &target); err != nil {
			return ""
		}
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
			return ""
		}
		u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
		u.ForceQuery = false
		// Re-encode the decoded path so percent-encoding cannot hide the token.
		if token != "" && strings.Contains(u.Path, token) {
			u.Path = strings.ReplaceAll(u.Path, token, "[REDACTED]")
			u.RawPath = ""
		}
		target = u.String()
	} else {
		if err := json.Unmarshal(test.Config["server"], &target); err != nil {
			return ""
		}
		host := target
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") && net.ParseIP(host[1:len(host)-1]) != nil {
			host = host[1 : len(host)-1]
		}
		if net.ParseIP(host) == nil && (len(host) > 253 || !reportHostname.MatchString(host)) {
			return ""
		}
		target = host
		var port int
		if json.Unmarshal(test.Config["port"], &port) == nil && port > 0 && port <= 65535 {
			target = net.JoinHostPort(host, strconv.Itoa(port))
		}
	}
	if utf8.RuneCountInString(target) > 2048 {
		return ""
	}
	return reportText(target, token, 2048)
}

func (c *apiClient) enrichAgents(agents []AgentResult, names map[string]string, testID string) {
	for index := range agents {
		agent := &agents[index]
		agent.AgentName = reportText(agent.AgentName, c.token, 160)
		if agent.AgentName == "" {
			agent.AgentName = names[agent.AgentID]
		}
		if agent.URL == "" {
			agent.URL = c.appLink(testID, agent.AgentID, agent.RoundID)
		}
	}
}

func (c *apiClient) appLink(testID, agentID, roundID string) string {
	// Only the documented public API origin has a known application origin.
	// Custom/staging origins must supply their own permitted appLink.
	if c.base.Scheme != "https" || !strings.EqualFold(c.base.Host, "api.thousandeyes.com") || c.base.Path != "/v7" {
		return ""
	}
	for _, id := range []string{c.aid, testID, agentID, roundID} {
		if len(id) > 32 || !numericID.MatchString(id) {
			return ""
		}
	}
	if roundID == "0" {
		return ""
	}
	u := url.URL{Scheme: "https", Host: "app.thousandeyes.com", Path: "/view/tests"}
	u.RawQuery = url.Values{"__a": {c.aid}, "testId": {testID}, "agentId": {agentID}, "roundId": {roundID}}.Encode()
	return u.String()
}
