package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/matgreaves/rig/explain"
)

func runExplain(args []string) error {
	filename, flagArgs := extractFile(args)

	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	var pretty bool
	fs.BoolVar(&pretty, "p", false, "pretty-print output (default is JSON)")

	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if filename == "" {
		if fs.NArg() > 0 {
			filename = fs.Arg(0)
		} else {
			return fmt.Errorf("missing JSONL file argument\n\nUsage: rig explain <file> [flags]")
		}
	}

	resolved, err := resolveLogFile(filename)
	if err != nil {
		return err
	}

	report, err := explain.AnalyzeFile(resolved)
	if err != nil {
		return err
	}

	if pretty {
		explain.Pretty(os.Stdout, report)
	} else {
		if err := explain.JSON(os.Stdout, report); err != nil {
			return err
		}
	}
	return nil
}
