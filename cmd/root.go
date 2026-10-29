// Copyright 2026 Cisco Systems, Inc. and its affiliates
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"

	"github.com/spf13/cobra"
	apicmd "github.com/thousandeyes/thousandeyes-cli/cmd/api"
	"github.com/thousandeyes/thousandeyes-cli/internal/config"
	"github.com/thousandeyes/thousandeyes-cli/internal/version"
)

var cfg config.Config

var rootCmd = newRootCommand()

const baseURLFlagName = "base-url"

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "thousandeyes",
		Short:        "CLI for ThousandEyes API workflows",
		Version:      version.Version,
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("token", "", "ThousandEyes API bearer token (or TE_TOKEN env var)")
	cmd.PersistentFlags().String(baseURLFlagName, "", "ThousandEyes platform base URL without /v7 (or TE_BASE_URL env var)")
	if err := cmd.PersistentFlags().MarkHidden(baseURLFlagName); err != nil {
		panic(err)
	}

	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if isCompletionCmd(cmd) {
			return nil
		}
		if err := apicmd.RootResourceRegistrationError(cmd.Root()); err != nil {
			return err
		}

		token, _ := cmd.Flags().GetString("token")
		baseURL, _ := cmd.Flags().GetString(baseURLFlagName)

		loaded, err := config.LoadWithOverrides(token, baseURL)
		if err != nil {
			return err
		}
		cfg = loaded
		return nil
	}
	apicmd.RegisterRootResourceCommands(cmd, func() config.Config { return cfg })
	cmd.AddCommand(newSyntheticsCommand(func() config.Config { return cfg }))
	apicmd.SetRootHelpFunc(cmd)
	configureCompletionCommand(cmd)
	return cmd
}

func Execute() error {
	return rootCmd.Execute()
}

func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}
