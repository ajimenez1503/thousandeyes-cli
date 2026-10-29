module github.com/thousandeyes/thousandeyes-cli

go 1.25.11

toolchain go1.27.3

require (
	github.com/spf13/cobra v1.8.1
	github.com/spf13/pflag v1.0.5
	golang.org/x/term v0.36.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/bitfield/gotestdox v0.2.2 // indirect
	github.com/dnephin/pflag v1.0.7 // indirect
	github.com/fatih/color v1.16.0 // indirect
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/telemetry v0.0.0-20260611141451-d61e87d5f4a3 // indirect
	golang.org/x/text v0.30.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
	golang.org/x/vuln v1.4.0 // indirect
	gotest.tools/gotestsum v1.12.0 // indirect
)

tool (
	golang.org/x/vuln/cmd/govulncheck
	gotest.tools/gotestsum
)
