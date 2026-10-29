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

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestUnknownCommandHintPrintedOnce(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "api", "-h")
	cmd.Dir = "."

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected nonzero exit for unknown command; output:\n%s", string(out))
	}

	output := string(out)
	if got := strings.Count(output, `Did you mean this?`); got != 1 {
		t.Fatalf("expected one hint block, got %d\noutput:\n%s", got, output)
	}
}
