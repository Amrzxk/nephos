package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

type resourceOptions struct {
	reference string
	cidr      string
	vpc       string
	zone      string
	output    string
	wait      bool
	timeout   time.Duration
	limit     int
	pageToken string
}

func parseResourceOptions(kind, verb string, args []string, stderr *os.File) (resourceOptions, error) {
	options := resourceOptions{output: "human", timeout: 2 * time.Minute}
	fs := flag.NewFlagSet(kind+" "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if verb != "list" {
		allowDashName := len(args) != 0 && args[0] == "--"
		if allowDashName {
			args = args[1:]
		}
		if len(args) == 0 || args[0] == "" || (!allowDashName && args[0][0] == '-') {
			return options, fmt.Errorf("%s %s requires a name or ID", kind, verb)
		}
		options.reference, args = args[0], args[1:]
	}
	fs.StringVar(&options.output, "o", "human", "output format: human or json")
	fs.StringVar(&options.output, "output", "human", "output format: human or json")
	switch verb {
	case "create":
		fs.StringVar(&options.cidr, "cidr-block", "", "IPv4 CIDR block")
		if kind == "subnet" {
			fs.StringVar(&options.vpc, "vpc", "", "VPC name or ID")
			fs.StringVar(&options.zone, "availability-zone", "", "local availability zone")
		}
		fs.BoolVar(&options.wait, "wait", false, "wait for available or failed")
		fs.DurationVar(&options.timeout, "timeout", 2*time.Minute, "wait timeout")
	case "delete":
		fs.BoolVar(&options.wait, "wait", false, "wait for resource removal")
		fs.DurationVar(&options.timeout, "timeout", 2*time.Minute, "wait timeout")
	case "list":
		fs.IntVar(&options.limit, "limit", 0, "page size, 1-100")
		fs.StringVar(&options.pageToken, "page-token", "", "next page token")
	case "describe":
	default:
		return options, fmt.Errorf("unknown %s command %q", kind, verb)
	}
	if err := fs.Parse(args); err != nil {
		return options, err
	}
	if fs.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments")
	}
	if options.output != "human" && options.output != "json" {
		return options, fmt.Errorf("output format must be human or json")
	}
	if (verb == "create" || verb == "delete") && options.timeout <= 0 {
		return options, fmt.Errorf("timeout must be positive")
	}
	if verb == "create" && options.cidr == "" {
		return options, fmt.Errorf("--cidr-block is required")
	}
	if verb == "create" && kind == "subnet" && (options.vpc == "" || options.zone == "") {
		return options, fmt.Errorf("--vpc and --availability-zone are required")
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

func runResource(ctx context.Context, kind string, args []string, stdout, stderr *os.File, cfg resourceConfig) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "nephos %s: command required\n", kind)
		return exitUsage
	}
	verb := args[0]
	options, err := parseResourceOptions(kind, verb, args[1:], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "nephos %s %s: %v\n", kind, verb, err)
		return exitUsage
	}
	api, cfg, err := newResourceClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "nephos %s %s: %v\n", kind, verb, err)
		return 1
	}
	if kind == "vpc" {
		err = runVPC(ctx, api, cfg, verb, options, stdout)
	} else {
		err = runSubnet(ctx, api, cfg, verb, options, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "nephos %s %s: %v\n", kind, verb, err)
		return 1
	}
	return exitOK
}

func writeResource(stdout *os.File, value any, output string) error {
	if output == "json" {
		return json.NewEncoder(stdout).Encode(value)
	}
	switch item := value.(type) {
	case client.Vpc:
		_, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.Id, item.Name, item.CidrBlock, item.State)
		return err
	case client.Subnet:
		_, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.Id, item.Name, item.VpcId, item.CidrBlock, item.State)
		return err
	case client.Instance:
		_, err := fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.Id, item.Name, item.SubnetId, item.PrivateIp, item.State)
		return err
	case client.InstancesPage:
		for i := range item.Items {
			if err := writeResource(stdout, item.Items[i], "human"); err != nil {
				return err
			}
		}
		if item.NextPageToken != nil {
			_, err := fmt.Fprintf(stdout, "Next page: %s\n", *item.NextPageToken)
			return err
		}
		return nil
	case client.VpcsPage:
		for i := range item.Items {
			if err := writeResource(stdout, item.Items[i], "human"); err != nil {
				return err
			}
		}
		if item.NextPageToken != nil {
			_, err := fmt.Fprintf(stdout, "Next page: %s\n", *item.NextPageToken)
			return err
		}
		return nil
	case client.SubnetsPage:
		for i := range item.Items {
			if err := writeResource(stdout, item.Items[i], "human"); err != nil {
				return err
			}
		}
		if item.NextPageToken != nil {
			_, err := fmt.Fprintf(stdout, "Next page: %s\n", *item.NextPageToken)
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported CLI resource output %T", value)
	}
}

func runVPC(ctx context.Context, api *client.ClientWithResponses, cfg resourceConfig, verb string, options resourceOptions, stdout *os.File) error {
	switch verb {
	case "create":
		key, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		var response *client.CreateVpcResponse
		for attempt := 0; attempt < 3; attempt++ {
			response, err = api.CreateVpcWithResponse(ctx, "default", &client.CreateVpcParams{IdempotencyKey: &key},
				client.CreateVpcRequest{Name: options.reference, CidrBlock: options.cidr})
			if err == nil && response.StatusCode() != 502 && response.StatusCode() != 503 && response.StatusCode() != 504 {
				break
			}
			if attempt == 2 {
				break
			}
			if err := retryDelay(ctx, attempt); err != nil {
				return err
			}
		}
		if err != nil {
			return fmt.Errorf("create VPC request: %w", err)
		}
		if response.JSON201 == nil {
			return responseError("create VPC", response)
		}
		vpc := *response.JSON201
		if options.wait {
			vpc, err = waitVPC(ctx, api, vpc.Id, false, options.timeout, cfg.pollInterval)
			if err != nil {
				return err
			}
		}
		return writeResource(stdout, vpc, options.output)
	case "list":
		params := &client.ListVpcsParams{}
		if options.limit > 0 {
			params.Limit = &options.limit
		}
		if options.pageToken != "" {
			params.PageToken = &options.pageToken
		}
		response, err := api.ListVpcsWithResponse(ctx, "default", params)
		if err != nil {
			return fmt.Errorf("list VPCs: %w", err)
		}
		if response.JSON200 == nil {
			return responseError("list VPCs", response)
		}
		return writeResource(stdout, *response.JSON200, options.output)
	case "describe", "delete":
		id, err := resolveVPC(ctx, api, options.reference)
		if err != nil {
			return err
		}
		if verb == "describe" {
			response, err := api.GetVpcWithResponse(ctx, "default", id)
			if err != nil {
				return fmt.Errorf("describe VPC: %w", err)
			}
			if response.JSON200 == nil {
				return responseError("describe VPC", response)
			}
			return writeResource(stdout, *response.JSON200, options.output)
		}
		response, err := api.DeleteVpcWithResponse(ctx, "default", id)
		if err != nil {
			return fmt.Errorf("delete VPC: %w", err)
		}
		if response.JSON202 == nil {
			return responseError("delete VPC", response)
		}
		if options.wait {
			if _, err := waitVPC(ctx, api, id, true, options.timeout, cfg.pollInterval); err != nil {
				return err
			}
			if options.output == "human" {
				_, err := fmt.Fprintf(stdout, "%s deleted\n", id)
				return err
			}
		}
		return writeResource(stdout, *response.JSON202, options.output)
	}
	return fmt.Errorf("unsupported VPC verb %q", verb)
}

func runSubnet(ctx context.Context, api *client.ClientWithResponses, cfg resourceConfig, verb string, options resourceOptions, stdout *os.File) error {
	switch verb {
	case "create":
		vpcID, err := resolveVPC(ctx, api, options.vpc)
		if err != nil {
			return err
		}
		key, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		var response *client.CreateSubnetResponse
		for attempt := 0; attempt < 3; attempt++ {
			response, err = api.CreateSubnetWithResponse(ctx, "default", &client.CreateSubnetParams{IdempotencyKey: &key},
				client.CreateSubnetRequest{Name: options.reference, VpcId: vpcID, CidrBlock: options.cidr,
					AvailabilityZone: client.CreateSubnetRequestAvailabilityZone(options.zone)})
			if err == nil && response.StatusCode() != 502 && response.StatusCode() != 503 && response.StatusCode() != 504 {
				break
			}
			if attempt == 2 {
				break
			}
			if err := retryDelay(ctx, attempt); err != nil {
				return err
			}
		}
		if err != nil {
			return fmt.Errorf("create subnet request: %w", err)
		}
		if response.JSON201 == nil {
			return responseError("create subnet", response)
		}
		subnet := *response.JSON201
		if options.wait {
			subnet, err = waitSubnet(ctx, api, subnet.Id, false, options.timeout, cfg.pollInterval)
			if err != nil {
				return err
			}
		}
		return writeResource(stdout, subnet, options.output)
	case "list":
		params := &client.ListSubnetsParams{}
		if options.limit > 0 {
			params.Limit = &options.limit
		}
		if options.pageToken != "" {
			params.PageToken = &options.pageToken
		}
		response, err := api.ListSubnetsWithResponse(ctx, "default", params)
		if err != nil {
			return fmt.Errorf("list subnets: %w", err)
		}
		if response.JSON200 == nil {
			return responseError("list subnets", response)
		}
		return writeResource(stdout, *response.JSON200, options.output)
	case "describe", "delete":
		id, err := resolveSubnet(ctx, api, options.reference)
		if err != nil {
			return err
		}
		if verb == "describe" {
			response, err := api.GetSubnetWithResponse(ctx, "default", id)
			if err != nil {
				return fmt.Errorf("describe subnet: %w", err)
			}
			if response.JSON200 == nil {
				return responseError("describe subnet", response)
			}
			return writeResource(stdout, *response.JSON200, options.output)
		}
		response, err := api.DeleteSubnetWithResponse(ctx, "default", id)
		if err != nil {
			return fmt.Errorf("delete subnet: %w", err)
		}
		if response.JSON202 == nil {
			return responseError("delete subnet", response)
		}
		if options.wait {
			if _, err := waitSubnet(ctx, api, id, true, options.timeout, cfg.pollInterval); err != nil {
				return err
			}
			if options.output == "human" {
				_, err := fmt.Fprintf(stdout, "%s deleted\n", id)
				return err
			}
		}
		return writeResource(stdout, *response.JSON202, options.output)
	}
	return fmt.Errorf("unsupported subnet verb %q", verb)
}
