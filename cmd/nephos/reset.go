package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

// runReset is the soft reset: the appliance keeps running, every Nephos
// resource in the default workspace is removed, and the result is verified
// by the leak checker before the command succeeds.
func runReset(ctx context.Context, args []string, stdout, stderr *os.File, cfg resourceConfig) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for teardown and the leak check (max 30m)")
	output := "human"
	fs.StringVar(&output, "o", "human", "output format: human or json")
	fs.StringVar(&output, "output", "human", "output format: human or json")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "nephos reset: unexpected positional arguments")
		return exitUsage
	}
	if output != "human" && output != "json" {
		fmt.Fprintln(stderr, "nephos reset: output format must be human or json")
		return exitUsage
	}
	seconds := int(math.Ceil(timeout.Seconds()))
	if *timeout <= 0 || seconds > 1800 {
		fmt.Fprintln(stderr, "nephos reset: --timeout must be between 1s and 30m")
		return exitUsage
	}
	// The server enforces the reset budget; the HTTP client only needs to
	// outlive it so that the server's own timeout answer arrives.
	api, _, err := newResourceClientWithTimeout(cfg, time.Duration(seconds)*time.Second+30*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "nephos reset: %v\n", err)
		return 1
	}
	if output == "human" {
		fmt.Fprintln(stderr, "Removing all instances, subnets and VPCs in workspace default...")
	}
	response, err := api.ResetWorkspaceWithResponse(ctx, "default", &client.ResetWorkspaceParams{Timeout: &seconds})
	if err != nil {
		fmt.Fprintf(stderr, "nephos reset: %v\n", err)
		return 1
	}
	if response.JSON200 != nil {
		if output == "json" {
			_ = json.NewEncoder(stdout).Encode(response.JSON200)
		} else {
			fmt.Fprintln(stdout, "Workspace default is empty; the leak check found no Nephos objects.")
		}
		return exitOK
	}
	if leak := response.JSON409; leak != nil && leak.LeakCheck != nil {
		if output == "json" {
			_ = json.NewEncoder(stdout).Encode(leak)
		}
		fmt.Fprintf(stderr, "nephos reset: %s: %s\n", leak.Code, leak.Message)
		for _, finding := range leak.LeakCheck.Findings {
			fmt.Fprintf(stderr, "  %s %s: %s\n", finding.Kind, finding.Identity, finding.Reason)
		}
		for _, inspection := range leak.LeakCheck.InspectionErrors {
			fmt.Fprintf(stderr, "  inspection error: %s\n", inspection)
		}
		return 1
	}
	fmt.Fprintf(stderr, "nephos reset: %v\n", responseError("reset", response))
	return 1
}
