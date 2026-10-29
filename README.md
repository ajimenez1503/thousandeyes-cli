# thousandeyes

`thousandeyes` is a Go CLI for interacting with the ThousandEyes API.
It is designed to be both human-usable and automation/agent-friendly.

## Features

- Discover shorthand **`resource`** / **`verb`** commands directly from root help and nested help
- JSON output mode for machine consumption (`--json`)

## Installation

Install latest release (Linux/macOS):

```bash
curl -fsSL https://raw.githubusercontent.com/thousandeyes/thousandeyes-cli/main/scripts/install.sh | sh
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/thousandeyes/thousandeyes-cli/main/scripts/install.sh | TE_VERSION=1.2.3 sh
```

By default, the script installs to `/usr/local/bin` when writable, otherwise `~/.local/bin`. Set `INSTALL_DIR` to override the destination.

The installer will overwrite the target binary. Running installation script will remove the existing `thousandeyes` binaries found on your `PATH`.

After install, the script prints shell-specific instructions for enabling tab completion.

## Build locally

Requirements:

- Go 1.25+
- A ThousandEyes bearer token

The module selects the patched Go 1.27.3 build toolchain automatically. The container uses the same version.

```bash
git clone https://github.com/thousandeyes/thousandeyes-cli
cd thousandeyes-cli
go build -o thousandeyes .
```

Install to PATH:

```bash
go build -o /usr/local/bin/thousandeyes .
```

## Quick Start

Set auth token:

```bash
export TE_TOKEN="<thousandeyes-bearer-token>"
```

Run:

```bash
thousandeyes --help
thousandeyes tests list
```

## Configuration

Global flags:

- `--token`: bearer token (overrides `TE_TOKEN`)

Environment variables:

- `TE_TOKEN`: ThousandEyes API bearer token used for authentication.
- `TE_BASE_URL`: ThousandEyes platform base URL without `/v7` (for example, `https://api.thousandeyes.com`).

## Shell Completion

Generate completion scripts:

```bash
thousandeyes completion bash
thousandeyes completion zsh
thousandeyes completion fish
thousandeyes completion powershell
```

Enable completion for your shell (pick one):

**bash**

```bash
source <(thousandeyes completion bash)
# or persist in ~/.bashrc:
echo 'source <(thousandeyes completion bash)' >> ~/.bashrc
```

**zsh**

```bash
# If zsh completion is not already enabled:
autoload -Uz compinit
compinit

source <(thousandeyes completion zsh)
# or persist in ~/.zshrc (after compinit):
echo 'source <(thousandeyes completion zsh)' >> ~/.zshrc
```

**fish**

```bash
mkdir -p ~/.config/fish/completions
thousandeyes completion fish > ~/.config/fish/completions/thousandeyes.fish
```

**PowerShell**

```powershell
thousandeyes completion powershell | Out-String | Invoke-Expression
# or persist in $PROFILE:
Add-Content $PROFILE 'thousandeyes completion powershell | Out-String | Invoke-Expression'
```

## Commands

### API

**Shorthand**

```text
thousandeyes <command-segment> [<command-segment> ...] [flags]
```

Use **`thousandeyes --help`** to see top-level command segments, then keep drilling down with **`thousandeyes <segment> --help`** until you reach the
verb command. If two commands share the same final verb token under one branch, that branch help lists the exact subcommand names to use.

**`thousandeyes <command path> --help`** shows path parameters, query parameters, and, when the call takes a JSON body, flags for top-level body fields
and a short example shape.

**How shorthand commands are generated**

- The CLI loads operations from `api/thousandeyes.yaml`.
- Command routing comes from `x-thousandeyes-cli-command` attributes on the effective OpenAPI spec (for example, from `api/thousandeyes.yaml` directly
  or via `api/thousandeyes.overlay.yaml`).
- Release binaries embed the spec and overlay so installed CLIs can load API commands without external `api/` files.
- Each value is a slash-separated command path. Example: `tests/http-server/list` maps to `thousandeyes tests http-server list`.
- Only operations with `x-thousandeyes-cli-command` are exposed as shorthand commands.
- Nested command groups are supported, so paths with 3+ segments create subcommand trees automatically.

**Operation parameter flags (shorthand commands)**

- Path and query parameters are exposed as first-class flags on each shorthand verb command.
- Use `thousandeyes <command path> --help` to see available parameter flags.
- Example: if a command path is `/alerts/{alertId}` and it supports query param `state`, use `--alertId <value>` and `--state <value>`.

**JSON request body (shorthand)**

Many verbs expose **one flag per top-level JSON field** (names look like **`--my-field`**). Build the request body by passing those field flags. For
array or object fields, pass the value as JSON on the flag (quoted in the shell).

Examples:

```bash
./thousandeyes --help
./thousandeyes tests list
./thousandeyes alerts get --alertId 2783
./thousandeyes alerts get --help
./thousandeyes account-groups create \
  --account-group-name "my-group"
./thousandeyes tests api create \
  --test-name "sample-api-test" \
  --type "api" \
  --requests '[{"url":"https://example.com","method":"GET"}]' \
  --predefined-variables '{"region":"eu-west"}'
```

## Synthetic checks for CI and progressive delivery

Run HTTP-server and agent-to-server instant tests from a repository JSON file:

```bash
export TE_TOKEN="<thousandeyes-bearer-token>"
thousandeyes synthetics run-tests \
  --config examples/synthetics/thousandeyes-ci.json \
  --json-report results.json \
  --junit-report results.xml
```

Replace the example account group, agent IDs, and targets with values from your account before running it. Every execution creates new instant tests and consumes the applicable ThousandEyes test units. The CLI does not edit scheduled tests or reuse another execution's instant test IDs.

The same command can run from a terminal, GitHub Actions, Jenkins, GitLab, or a Kubernetes Job. The runner orchestrates tests; selected ThousandEyes Cloud or Enterprise Agents perform the measurements. Private targets require an Enterprise Agent with network access to them. The command does not provide a testing tunnel or install agents.

### Configuration and evaluation

The version 1 JSON format contains `accountGroupId`, an optional suite `timeout` (default `5m`), an optional `pollInterval` (default `5s`), and a nonempty `tests` array. Each test defines:

| Field | Meaning |
| --- | --- |
| `name` | Unique name within the suite. |
| `type` | `http-server` or `agent-to-server`. |
| `config` | Native instant-test request payload, including its target and `agents`. Do not include read-only fields such as `type` or `testId`. |
| `executionRule` | `blocking` (default), `non-blocking`, or `skipped`. |
| `thresholds` | Optional criteria shown below. |

| Type | Default gate | Optional thresholds |
| --- | --- | --- |
| HTTP server | A response code from 200 through 399, with no reported test error. | `expectedResponseCodes` replaces the CI's accepted codes; `maxResponseTime` limits time to first byte in milliseconds; `maxTotalTime` limits total time in milliseconds. |
| Agent to server | Measured end-to-end packet loss is zero. | `maxLoss` is a percentage; `maxLatency` limits average RTT in milliseconds; `maxJitter` limits jitter in milliseconds. |

Every configured agent must return applicable results from one coherent round. Missing values remain pending until the suite deadline; they never become zero or success. A known primary measurement failure can fail immediately without waiting for timing values that may never arrive. Network diagnostics attached to HTTP tests do not automatically become additional gates. When testing an expected non-2xx/3xx response, also align the native payload's `desiredStatusCode` setting; a native test error remains a failed check even if its response code appears in `expectedResponseCodes`.

For agent-to-server creation, set `server` to the hostname or IP address and supply a separate integer `port` for TCP or UDP targets. The API response may combine them as `hostname:port`; that response format is not accepted as the creation target in the verified STG API.

This first slice supports individual Cloud and Enterprise Agents. Cluster-agent completeness and network directions other than `to-target` are not supported. The full configuration is validated before submitting tests. Unsupported test types, duplicate agents, incompatible thresholds, and invalid durations are rejected.

### Results and exit status

- Exit `0`: every blocking check passed, or was explicitly skipped. Non-blocking assertion failures remain visible in reports.
- Exit nonzero: a blocking assertion failed, execution was incomplete or timed out, an API/configuration error occurred, cancellation was requested, or a report could not be written. Infrastructure failures remain blocking even for a non-blocking test.
- `--json` prints the sanitized report to standard output. `--json-report` and `--junit-report` write files using the same results. Parent directories must already exist.
- JUnit contains a case per agent, or a case for a test with no agent outcomes. An additional execution-error case preserves any test-level failure not represented by agent outcomes. Non-blocking assertion failures are represented as JUnit failures and have an `executionRule` property, while the CLI exit status follows the configured rule. Consumers that independently gate on JUnit must account for that distinction.

Reports contain run/test/agent identities, status, selected numeric measurements, and permitted platform links. They do not include native request payloads, response headers, credentials, or arbitrary API error bodies. Treat configuration files containing target credentials as secrets and supply `TE_TOKEN` through your CI's secret mechanism.

`--account-group-id`, `--timeout`, and `--poll-interval` override the corresponding suite settings. Each invocation is bounded by one suite deadline. The engine handles rate-limit responses within that deadline and does not blindly retry creation requests whose outcome is ambiguous.

### Container and Argo Rollouts

Build a local image:

```bash
docker build -t thousandeyes-cli:synthetic-ci .
docker run --rm -e TE_TOKEN \
  -v "$PWD/examples/synthetics:/config:ro" \
  thousandeyes-cli:synthetic-ci synthetics run-tests \
  --config /config/thousandeyes-ci.json --json
```

For Argo Rollouts, see [`examples/argo-rollouts/analysis-template.yaml`](examples/argo-rollouts/analysis-template.yaml) and [`examples/argo-rollouts/rollout-step.yaml`](examples/argo-rollouts/rollout-step.yaml). Build and push the image to your own registry, create a Secret with a `token` key, and create the suite ConfigMap:

```bash
kubectl create configmap thousandeyes-canary-suite \
  --from-file=thousandeyes-ci.json=examples/synthetics/thousandeyes-ci.json
```

Apply the AnalysisTemplate and add the example analysis step to the Rollout. Pass the image, account group, suite ConfigMap, and optional Secret name as template arguments. A ClusterAnalysisTemplate can use the same spec when a shared template is appropriate.

Configure the suite targets to reach the canary or preview revision explicitly. A shared weighted endpoint may route checks to the stable deployment. A Job running inside Kubernetes does not give remote agents access to cluster-internal Services; select connected Enterprise Agents for those targets. The example disables pod retries, uses one measurement, and sets the outer Job deadline longer than the CLI's five-minute timeout.

### Validation status and next adapters

The merged implementation passed GitHub CI, snapshot packaging, mock API integration tests, and process-level exit-code tests. A controlled STG pilot on 2026-10-29 verified HTTP status and timing checks, TCP network loss and latency checks, successful exit status, and a deliberate blocking assertion failure from one Cloud Agent. The HTTP target was an unauthenticated STG API endpoint with an expected `401` response; this verifies measurement and evaluation behavior rather than an authenticated application journey.

The Argo examples were exercised with a real local Argo Rollouts controller and a mock ThousandEyes API. Healthy results promoted the candidate; HTTP failures, network failures, and incomplete measurements aborted it and retained the stable revision. Live ThousandEyes execution from an Argo Job, Enterprise Agents, minimum required API permissions, and production canary targeting remain unverified.

Saved-test cloning, label selection, variable overrides, the standalone GitHub marketplace Action, API/DNS/browser/voice adapters, and runner-local connectivity are subsequent work. The shared execution engine is the foundation for those integrations.

## Project Structure

The repo is organized by command area:

- `cmd/root.go` wires the CLI together and loads shared config.
- `cmd/api/` contains shorthand OpenAPI-backed routing, request building, and API-specific tests.
- `internal/apispec/` parses and caches the OpenAPI spec used by the API shorthand commands.
- `internal/cliurls/` holds shared URL-building helpers.
- `internal/teapi/` contains the generated and raw ThousandEyes API clients.

## Development

Generate the typed OpenAPI client:

```bash
go generate ./...
```

Run checks (CI-aligned):

```bash
make ci
```

Run individual checks:

```bash
make validate
make test
make build
```

## License

Distributed under the `Apache License 2.0`. See [LICENSE](LICENSE) for more information.

## Releasing a new version

Releases are built with [GoReleaser](https://goreleaser.com/) via the **Release** GitHub Actions workflow. The workflow uses GitHub's default
`GITHUB_TOKEN` and runs only when you trigger it manually; pushing a tag alone does not start a release.

1. **Choose a version** using semantic versioning (for example `1.2.3`).

2. **Run the workflow** in GitHub: **Actions** → **Release** → **Run workflow** → select the branch or commit to ship (for example `main`) → enter
   the tag (e.g. `1.2.3`) → **Run workflow**.

The workflow creates and pushes the tag on the selected ref, then publishes a GitHub Release with binaries and checksums, as configured in
`.goreleaser.yaml`.
