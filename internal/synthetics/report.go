// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func WriteJSON(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func WriteSummary(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "Synthetic run %s\n", report.RunID); err != nil {
		return err
	}
	for _, test := range report.Tests {
		if _, err := fmt.Fprintf(w, "%s %s (%s, %s)\n", strings.ToUpper(test.Status), test.Name, test.Type, test.ExecutionRule); err != nil {
			return err
		}
		if test.Message != "" {
			if _, err := fmt.Fprintf(w, "  %s\n", test.Message); err != nil {
				return err
			}
		}
		for _, agent := range test.Agents {
			if _, err := fmt.Fprintf(w, "  Agent %s: %s", agent.AgentID, agent.Status); err != nil {
				return err
			}
			if agent.Message != "" {
				if _, err := fmt.Fprintf(w, " (%s)", agent.Message); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
			if agent.URL != "" {
				if _, err := fmt.Fprintf(w, "    %s\n", agent.URL); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type junitSuite struct {
	XMLName    xml.Name        `xml:"testsuite"`
	Name       string          `xml:"name,attr"`
	Tests      int             `xml:"tests,attr"`
	Failures   int             `xml:"failures,attr"`
	Errors     int             `xml:"errors,attr"`
	Skipped    int             `xml:"skipped,attr"`
	Time       string          `xml:"time,attr"`
	Properties []junitProperty `xml:"properties>property"`
	Cases      []junitCase     `xml:"testcase"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitCase struct {
	Name       string          `xml:"name,attr"`
	Classname  string          `xml:"classname,attr"`
	Properties []junitProperty `xml:"properties>property"`
	Failure    *junitOutcome   `xml:"failure,omitempty"`
	Error      *junitOutcome   `xml:"error,omitempty"`
	Skipped    *junitOutcome   `xml:"skipped,omitempty"`
	SystemOut  string          `xml:"system-out,omitempty"`
}

type junitOutcome struct {
	Type    string `xml:"type,attr,omitempty"`
	Message string `xml:"message,attr,omitempty"`
}

func WriteJUnit(w io.Writer, report Report) error {
	seconds := report.FinishedAt.Sub(report.StartedAt).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	suite := junitSuite{
		Name: "ThousandEyes synthetic CI", Time: strconv.FormatFloat(seconds, 'f', 3, 64),
		Properties: []junitProperty{{Name: "runId", Value: report.RunID}},
	}
	for _, test := range report.Tests {
		executionErrorRepresented := false
		agents := test.Agents
		if len(agents) == 0 {
			agents = []AgentResult{{Status: test.Status, Message: test.Message}}
		}
		for _, agent := range agents {
			result := junitCase{
				Name: test.Name, Classname: test.Type,
				Properties: []junitProperty{
					{Name: "executionRule", Value: test.ExecutionRule},
					{Name: "testId", Value: test.TestID},
				},
			}
			if agent.AgentID != "" {
				result.Name += " / agent " + agent.AgentID
				result.Properties = append(result.Properties, junitProperty{Name: "agentId", Value: agent.AgentID})
			}
			if agent.RoundID != "" {
				result.Properties = append(result.Properties, junitProperty{Name: "roundId", Value: agent.RoundID})
			}
			status, message := agent.Status, agent.Message
			if status == "" {
				status, message = test.Status, test.Message
			}
			switch status {
			case "passed":
			case "skipped":
				result.Skipped = &junitOutcome{Message: message}
				suite.Skipped++
			case "failed":
				result.Failure = &junitOutcome{Type: test.ExecutionRule, Message: message}
				suite.Failures++
			default:
				result.Error = &junitOutcome{Type: status, Message: message}
				suite.Errors++
				executionErrorRepresented = true
			}
			if len(agent.Metrics) > 0 {
				metrics, err := json.Marshal(agent.Metrics)
				if err != nil {
					return fmt.Errorf("encode JUnit metrics: %w", err)
				}
				result.SystemOut = string(metrics)
			}
			suite.Cases = append(suite.Cases, result)
		}
		// A suite deadline or pagination error can occur after usable agent
		// results arrived. Preserve that execution failure in machine reports.
		if (test.Status == "error" || test.Status == "timed-out") && !executionErrorRepresented {
			suite.Cases = append(suite.Cases, junitCase{
				Name: test.Name + " / execution", Classname: test.Type,
				Error: &junitOutcome{Type: test.Status, Message: test.Message},
			})
			suite.Errors++
		}
	}
	suite.Tests = len(suite.Cases)
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(suite); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}
