package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func runSummary(args []string) error {
	var pattern string
	var flags ciFlags

	for _, a := range args {
		switch a {
		case "--help", "-h":
			printSummaryUsage()
			return nil
		case "--failed":
			flags.failed = true
		case "--passed":
			flags.passed = true
		case "-p":
			flags.pretty = true
		case "-v":
			flags.verbose = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "rig summary: unknown flag %q\n\n", a)
				printSummaryUsage()
				os.Exit(1)
			}
			if pattern != "" {
				fmt.Fprintf(os.Stderr, "rig summary: unexpected argument %q\n\n", a)
				printSummaryUsage()
				os.Exit(1)
			}
			pattern = a
		}
	}

	return runSummaryReport(nil, rigdata.LogDir(), pattern, flags)
}

func printSummaryUsage() {
	fmt.Fprintf(os.Stderr, `Usage: rig summary [pattern] [flags]

Summarize local test results from .rig/logs/.

Pattern (optional):
  A test name or glob to filter results (e.g. S3, *Order*).

Flags:
  --failed      only show failed/crashed tests
  --passed      only show passed tests
  -p            pretty-print (default is JSON)
  -v            verbose — full explain output per failed test (requires -p)

Examples:
  rig summary                       # summary of all local tests (JSON)
  rig summary -p                    # pretty-printed
  rig summary --failed -p -v        # verbose failure details
  rig summary S3 -p                 # filter to tests matching "S3"
`)
}
