package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

type instanceOptions struct {
	reference, subnet, output, pageToken string
	wait                                 bool
	timeout                              time.Duration
	limit                                int
}

func parseInstanceOptions(verb string, args []string, stderr *os.File) (instanceOptions, error) {
	options := instanceOptions{output: "human", timeout: 2 * time.Minute}
	fs := flag.NewFlagSet("instance "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if verb != "list" {
		allowDash := len(args) > 0 && args[0] == "--"
		if allowDash {
			args = args[1:]
		}
		if len(args) == 0 || args[0] == "" || (!allowDash && args[0][0] == '-') {
			return options, fmt.Errorf("instance %s requires a name or ID", verb)
		}
		options.reference, args = args[0], args[1:]
	}
	fs.StringVar(&options.output, "o", "human", "output format: human or json")
	fs.StringVar(&options.output, "output", "human", "output format: human or json")
	switch verb {
	case "run", "terminate":
		if verb == "run" {
			fs.StringVar(&options.subnet, "subnet", "", "subnet name or ID")
		}
		fs.BoolVar(&options.wait, "wait", false, "wait for running or removal")
		fs.DurationVar(&options.timeout, "timeout", 2*time.Minute, "wait timeout")
	case "list":
		fs.IntVar(&options.limit, "limit", 0, "page size, 1-100")
		fs.StringVar(&options.pageToken, "page-token", "", "next page token")
	case "describe":
	default:
		return options, fmt.Errorf("unknown instance command %q", verb)
	}
	if err := fs.Parse(args); err != nil {
		return options, err
	}
	if fs.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments")
	}
	if options.output != "json" && options.output != "human" {
		return options, fmt.Errorf("output format must be human or json")
	}
	if options.timeout <= 0 {
		return options, fmt.Errorf("timeout must be positive")
	}
	if verb == "run" && options.subnet == "" {
		return options, fmt.Errorf("--subnet is required")
	}
	explicitLimit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			explicitLimit = true
		}
	})
	if verb == "list" && ((explicitLimit && options.limit < 1) || options.limit > 100) {
		return options, fmt.Errorf("--limit must be 1-100")
	}
	return options, nil
}

func runInstance(ctx context.Context, args []string, stdout, stderr *os.File, cfg resourceConfig) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "nephos instance: command required")
		return exitUsage
	}
	options, err := parseInstanceOptions(args[0], args[1:], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "nephos instance: %v\n", err)
		return exitUsage
	}
	api, cfg, err := newResourceClient(cfg)
	if err == nil {
		err = instanceCommand(ctx, api, cfg, args[0], options, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "nephos instance %s: %v\n", args[0], err)
		return 1
	}
	return exitOK
}

func instanceCommand(ctx context.Context, api *client.ClientWithResponses, cfg resourceConfig, verb string, options instanceOptions, stdout *os.File) error {
	switch verb {
	case "run":
		subnetID, err := resolveSubnet(ctx, api, options.subnet)
		if err != nil {
			return err
		}
		key, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		var response *client.RunInstanceResponse
		for attempt := 0; attempt < 3; attempt++ {
			response, err = api.RunInstanceWithResponse(ctx, "default", &client.RunInstanceParams{IdempotencyKey: &key}, client.RunInstanceRequest{Name: options.reference, SubnetId: subnetID})
			if err == nil && response.StatusCode() != 502 && response.StatusCode() != 503 && response.StatusCode() != 504 {
				break
			}
			if attempt < 2 {
				if err := retryDelay(ctx, attempt); err != nil {
					return err
				}
			}
		}
		if err != nil {
			return fmt.Errorf("run instance request: %w", err)
		}
		if response.JSON201 == nil {
			return responseError("run instance", response)
		}
		instance := *response.JSON201
		if options.wait {
			instance, err = waitInstance(ctx, api, instance.Id, false, options.timeout, cfg.pollInterval)
			if err != nil {
				return err
			}
		}
		return writeResource(stdout, instance, options.output)
	case "list":
		params := &client.ListInstancesParams{}
		if options.limit > 0 {
			params.Limit = &options.limit
		}
		if options.pageToken != "" {
			params.PageToken = &options.pageToken
		}
		response, err := api.ListInstancesWithResponse(ctx, "default", params)
		if err != nil {
			return fmt.Errorf("list instances: %w", err)
		}
		if response.JSON200 == nil {
			return responseError("list instances", response)
		}
		return writeResource(stdout, *response.JSON200, options.output)
	case "describe", "terminate":
		id, err := resolveInstance(ctx, api, options.reference)
		if err != nil {
			return err
		}
		if verb == "describe" {
			response, err := api.GetInstanceWithResponse(ctx, "default", id)
			if err != nil {
				return fmt.Errorf("describe instance: %w", err)
			}
			if response.JSON200 == nil {
				return responseError("describe instance", response)
			}
			return writeResource(stdout, *response.JSON200, options.output)
		}
		response, err := api.TerminateInstanceWithResponse(ctx, "default", id)
		if err != nil {
			return fmt.Errorf("terminate instance: %w", err)
		}
		if response.JSON202 == nil {
			return responseError("terminate instance", response)
		}
		if options.wait {
			if _, err := waitInstance(ctx, api, id, true, options.timeout, cfg.pollInterval); err != nil {
				return err
			}
			if options.output == "human" {
				_, err := fmt.Fprintf(stdout, "%s terminated\n", id)
				return err
			}
		}
		return writeResource(stdout, *response.JSON202, options.output)
	}
	return fmt.Errorf("unknown instance command %q", verb)
}
