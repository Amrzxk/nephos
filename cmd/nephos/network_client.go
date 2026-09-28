package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

const defaultEndpoint = "http://127.0.0.1:7788"

type resourceConfig struct {
	endpoint       string
	credentialPath string
	pollInterval   time.Duration
}

func (cfg resourceConfig) normalized() (resourceConfig, error) {
	if cfg.endpoint == "" {
		cfg.endpoint = defaultEndpoint
	}
	parsed, err := url.Parse(cfg.endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return cfg, fmt.Errorf("API endpoint must be an HTTP loopback address")
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		return cfg, fmt.Errorf("API endpoint must be an HTTP loopback address")
	}
	if cfg.credentialPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cfg, fmt.Errorf("find home directory: %w", err)
		}
		cfg.credentialPath = filepath.Join(home, ".nephos", "credentials")
	}
	if cfg.pollInterval <= 0 {
		cfg.pollInterval = 500 * time.Millisecond
	}
	return cfg, nil
}

func loadCredential(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read ~/.nephos/credentials: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 128 {
		return "", fmt.Errorf("credential file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read credential file: %w", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(data))
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != string(data) {
		return "", fmt.Errorf("credential file is malformed")
	}
	return string(data), nil
}

func newResourceClient(cfg resourceConfig) (*client.ClientWithResponses, resourceConfig, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, cfg, err
	}
	token, err := loadCredential(cfg.credentialPath)
	if err != nil {
		return nil, cfg, err
	}
	httpClient := &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	api, err := client.NewClientWithResponses(cfg.endpoint, client.WithHTTPClient(httpClient),
		client.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}))
	if err != nil {
		return nil, cfg, fmt.Errorf("create API client: %w", err)
	}
	return api, cfg, nil
}

type apiResponse interface {
	StatusCode() int
	GetBody() []byte
}

func responseError(operation string, response apiResponse) error {
	if response == nil {
		return fmt.Errorf("%s: no API response", operation)
	}
	var body client.Error
	if json.Unmarshal(response.GetBody(), &body) == nil && body.Code != "" {
		return fmt.Errorf("%s: %s: %s", operation, body.Code, body.Message)
	}
	return fmt.Errorf("%s: HTTP %d", operation, response.StatusCode())
}

func newIdempotencyKey() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate request idempotency key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func retryDelay(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * 100 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func looksLikeID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+17 {
		return false
	}
	for _, digit := range value[len(prefix):] {
		if digit < '0' || digit > '9' {
			if digit < 'a' || digit > 'f' {
				return false
			}
		}
	}
	return true
}

func resolveVPC(ctx context.Context, api *client.ClientWithResponses, reference string) (string, error) {
	if looksLikeID(reference, "vpc-") {
		response, err := api.GetVpcWithResponse(ctx, "default", reference)
		if err != nil {
			return "", fmt.Errorf("resolve VPC ID: %w", err)
		}
		if response.JSON200 != nil {
			return reference, nil
		}
		if response.StatusCode() != http.StatusNotFound {
			return "", responseError("resolve VPC ID", response)
		}
		// Legal case-sensitive names can themselves look like an AWS-style ID.
	}
	var token *string
	seen := make(map[string]struct{})
	match := ""
	for {
		limit := 100
		response, err := api.ListVpcsWithResponse(ctx, "default", &client.ListVpcsParams{Limit: &limit, PageToken: token})
		if err != nil {
			return "", fmt.Errorf("resolve VPC name: %w", err)
		}
		if response.JSON200 == nil {
			return "", responseError("resolve VPC name", response)
		}
		for i := range response.JSON200.Items {
			item := &response.JSON200.Items[i]
			if item.Name == reference {
				if match != "" {
					return "", fmt.Errorf("VPC name %q is ambiguous", reference)
				}
				match = item.Id
			}
		}
		token = response.JSON200.NextPageToken
		if token == nil || *token == "" {
			break
		}
		if _, repeated := seen[*token]; repeated {
			return "", fmt.Errorf("VPC list repeated a page token")
		}
		seen[*token] = struct{}{}
	}
	if match == "" {
		return "", fmt.Errorf("VPC name %q not found", reference)
	}
	return match, nil
}

func resolveSubnet(ctx context.Context, api *client.ClientWithResponses, reference string) (string, error) {
	if looksLikeID(reference, "subnet-") {
		response, err := api.GetSubnetWithResponse(ctx, "default", reference)
		if err != nil {
			return "", fmt.Errorf("resolve subnet ID: %w", err)
		}
		if response.JSON200 != nil {
			return reference, nil
		}
		if response.StatusCode() != http.StatusNotFound {
			return "", responseError("resolve subnet ID", response)
		}
	}
	var token *string
	seen := make(map[string]struct{})
	match := ""
	for {
		limit := 100
		response, err := api.ListSubnetsWithResponse(ctx, "default", &client.ListSubnetsParams{Limit: &limit, PageToken: token})
		if err != nil {
			return "", fmt.Errorf("resolve subnet name: %w", err)
		}
		if response.JSON200 == nil {
			return "", responseError("resolve subnet name", response)
		}
		for i := range response.JSON200.Items {
			item := &response.JSON200.Items[i]
			if item.Name == reference {
				if match != "" {
					return "", fmt.Errorf("subnet name %q is ambiguous", reference)
				}
				match = item.Id
			}
		}
		token = response.JSON200.NextPageToken
		if token == nil || *token == "" {
			break
		}
		if _, repeated := seen[*token]; repeated {
			return "", fmt.Errorf("subnet list repeated a page token")
		}
		seen[*token] = struct{}{}
	}
	if match == "" {
		return "", fmt.Errorf("subnet name %q not found", reference)
	}
	return match, nil
}
