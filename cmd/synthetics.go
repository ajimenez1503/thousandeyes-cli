// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/thousandeyes/thousandeyes-cli/internal/config"
	"github.com/thousandeyes/thousandeyes-cli/internal/synthetics"
)

func newSyntheticsCommand(getConfig func() config.Config) *cobra.Command {
	group := &cobra.Command{Use: "synthetics", Short: "Run synthetic tests as CI checks"}
	// Keep inherited authentication and output flags visible in Cobra help.
	group.SetHelpFunc(group.HelpFunc())
	var configPath, accountGroupID, jsonPath, junitPath string
	var timeout, pollInterval time.Duration
	var jsonOutput bool
	run := &cobra.Command{
		Use: "run-tests", Short: "Create instant tests, wait for results, and evaluate CI checks",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("timeout") && timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if cmd.Flags().Changed("poll-interval") && pollInterval <= 0 {
				return errors.New("--poll-interval must be positive")
			}
			suite, err := synthetics.LoadConfig(configPath)
			if err != nil {
				return err
			}
			if err := validateReportPaths(configPath, jsonPath, junitPath); err != nil {
				return err
			}
			cfg := getConfig()
			report, runErr := synthetics.Run(cmd.Context(), suite, synthetics.Options{
				BaseURL: cfg.BaseURL, Token: cfg.Token, AccountGroupID: accountGroupID,
				Timeout: timeout, PollInterval: pollInterval,
			})
			var outputErrs []error
			if jsonOutput {
				outputErrs = append(outputErrs, synthetics.WriteJSON(cmd.OutOrStdout(), report))
			} else {
				outputErrs = append(outputErrs, synthetics.WriteSummary(cmd.OutOrStdout(), report))
			}
			if jsonPath != "" {
				outputErrs = append(outputErrs, writeReportFile(jsonPath, func(w io.Writer) error { return synthetics.WriteJSON(w, report) }))
			}
			if junitPath != "" {
				outputErrs = append(outputErrs, writeReportFile(junitPath, func(w io.Writer) error { return synthetics.WriteJUnit(w, report) }))
			}
			if outputErr := errors.Join(outputErrs...); outputErr != nil {
				return errors.Join(runErr, outputErr)
			}
			if runErr != nil {
				return runErr
			}
			if report.Failed() {
				return errors.New("synthetic CI checks failed; see the report for details")
			}
			return nil
		},
	}
	run.Flags().StringVar(&configPath, "config", "", "Path to the version 1 synthetic suite JSON file")
	_ = run.MarkFlagRequired("config")
	run.Flags().StringVar(&accountGroupID, "account-group-id", "", "Override the account group used for this execution")
	run.Flags().DurationVar(&timeout, "timeout", 0, "Override the suite timeout (for example 5m)")
	run.Flags().DurationVar(&pollInterval, "poll-interval", 0, "Override the result polling interval (for example 5s)")
	run.Flags().StringVar(&jsonPath, "json-report", "", "Write a sanitized JSON report to this file")
	run.Flags().StringVar(&junitPath, "junit-report", "", "Write a JUnit XML report to this file")
	group.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Print the sanitized CI report as JSON")
	group.AddCommand(run)
	return group
}

func validateReportPaths(configPath string, reportPaths ...string) error {
	seen := map[string]bool{}
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	seen[configAbs] = true
	for _, path := range reportPaths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if seen[abs] {
			return errors.New("configuration and report files must use different paths")
		}
		seen[abs] = true
	}
	return nil
}

func writeReportFile(path string, write func(io.Writer) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".thousandeyes-report-*")
	if err != nil {
		return fmt.Errorf("create report %q: %w", path, err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := write(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("write report %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close report %q: %w", path, err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("save report %q: %w", path, err)
	}
	return nil
}
