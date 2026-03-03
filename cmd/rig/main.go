package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "traffic":
		if err := runTraffic(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig traffic: %v\n", err)
			os.Exit(1)
		}
	case "logs":
		if err := runLogs(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig logs: %v\n", err)
			os.Exit(1)
		}
	case "ls":
		if err := runLs(os.Args[2:]); err != nil {
			if err != errNoResults {
				fmt.Fprintf(os.Stderr, "rig ls: %v\n", err)
			}
			os.Exit(1)
		}
	case "explain":
		if err := runExplain(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig explain: %v\n", err)
			os.Exit(1)
		}
	case "summary":
		if err := runSummary(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig summary: %v\n", err)
			os.Exit(1)
		}
	case "ci":
		if err := runCi(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig ci: %v\n", err)
			os.Exit(1)
		}
	case "prune":
		if err := runPrune(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "rig prune: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "rig: unknown command %q\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: rig <command> [flags]

Commands:
  traffic <file>         Inspect traffic captured by rigd
  logs    <file>         View service logs
  ls      [pattern]      List recent log files
  explain <file>         Analyze failure from event log
  summary [pattern]      Summarize local test results
  ci      [target]       Analyze CI run artifacts (requires gh CLI)
  prune                  Prune stale cache entries and logs

Run 'rig <command> --help' for command-specific flags.
`)
}
