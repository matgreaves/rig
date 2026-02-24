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
  traffic <file.jsonl>   Inspect traffic captured by rigd
  logs    <file.jsonl>   View service logs

Run 'rig <command> --help' for command-specific flags.
`)
}
