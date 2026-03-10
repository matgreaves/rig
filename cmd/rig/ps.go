package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/matgreaves/rig/cmd/rig/rigdata"
)

func runPs(args []string) error {
	addr, err := rigdata.ServerAddr(RigdVersion)
	if err != nil {
		return err
	}

	entries, err := rigdata.FetchEnvironments(addr)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No active environments.")
		return nil
	}

	for i, e := range entries {
		resolved, err := rigdata.FetchResolved(addr, e.ID)
		if err != nil {
			continue
		}
		if i > 0 {
			fmt.Println()
		}
		renderEnvironment(e, resolved)
	}

	return nil
}

func renderEnvironment(entry rigdata.PsEntry, env *rigdata.ResolvedEnv) {
	fmt.Printf("%s  %s  expires in %s\n", bold(entry.Name), dim(entry.ID), entry.RemainingTTL)

	svcNames := make([]string, 0, len(env.Services))
	for name := range env.Services {
		svcNames = append(svcNames, name)
	}
	sort.Strings(svcNames)

	for _, svcName := range svcNames {
		svc := env.Services[svcName]

		ingNames := make([]string, 0, len(svc.Ingresses))
		for name := range svc.Ingresses {
			ingNames = append(ingNames, name)
		}
		sort.Strings(ingNames)

		for _, ingName := range ingNames {
			ep := svc.Ingresses[ingName]

			label := svcName
			if ingName != "default" {
				label = svcName + "/" + ingName
			}

			url := rigdata.ConnectionURL(ep)
			fmt.Printf("  %-20s  %s\n", label, url)
		}
	}
}
