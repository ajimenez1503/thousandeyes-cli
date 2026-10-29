// Copyright 2026 Cisco Systems, Inc. and its affiliates
// SPDX-License-Identifier: Apache-2.0

package synthetics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thousandeyes/thousandeyes-cli/internal/cliurls"
	"github.com/thousandeyes/thousandeyes-cli/internal/version"
)

type apiClient struct {
	base         *url.URL
	token        string
	aid          string
	pollInterval time.Duration
	http         *http.Client
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("ThousandEyes API returned HTTP %d", e.code) }

func newAPIClient(options Options, pollInterval time.Duration) (*apiClient, error) {
	base, err := url.Parse(cliurls.APIBaseV7(options.BaseURL))
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid ThousandEyes API base URL")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, fmt.Errorf("a ThousandEyes API token is required")
	}
	return &apiClient{
		base: base, token: options.Token, aid: options.AccountGroupID, pollInterval: pollInterval,
		http: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *apiClient) endpoint(path string) string {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	return u.String()
}

func (c *apiClient) resultURL(link, expectedPath string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("invalid result link")
	}
	reference := *c.base
	reference.Path = expectedPath
	u = reference.ResolveReference(u)
	if u.Scheme != c.base.Scheme || !strings.EqualFold(u.Host, c.base.Host) || u.User != nil || u.Fragment != "" || u.Path != expectedPath || u.RawPath != "" {
		return "", fmt.Errorf("unsafe result link; result links must use the configured API origin and test endpoint")
	}
	query := u.Query()
	if c.aid != "" {
		query.Set("aid", c.aid)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *apiClient) do(ctx context.Context, method, endpoint string, payload []byte, output any) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid API endpoint")
	}
	query := u.Query()
	if c.aid != "" {
		query.Set("aid", c.aid)
	}
	if method == http.MethodPost {
		query.Set("expand", "agent")
	}
	u.RawQuery = query.Encode()
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build ThousandEyes API request")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Accept", "application/hal+json, application/json")
		request.Header.Set("User-Agent", version.UserAgent())
		if payload != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if method == http.MethodGet && attempt < 2 {
				if err := waitContext(ctx, c.retryDelay(nil, attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("ThousandEyes API request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read ThousandEyes API response")
		}
		if len(body) > 4<<20 {
			return fmt.Errorf("ThousandEyes API response exceeded size limit")
		}
		if response.StatusCode == http.StatusTooManyRequests || (method == http.MethodGet && response.StatusCode >= 500 && response.StatusCode <= 599 && attempt < 2) {
			if err := waitContext(ctx, c.retryDelay(response.Header, attempt)); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &statusError{code: response.StatusCode}
		}
		if err := json.Unmarshal(body, output); err != nil {
			return fmt.Errorf("invalid JSON in ThousandEyes API response")
		}
		return nil
	}
}

func (c *apiClient) retryDelay(headers http.Header, attempt int) time.Duration {
	delay := c.pollInterval
	if delay > time.Second {
		delay = time.Second
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	if attempt > 6 {
		attempt = 6
	}
	delay *= 1 << attempt
	if retryAfter := headers.Get("Retry-After"); retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 && seconds <= 86400 {
			if candidate := time.Duration(seconds) * time.Second; candidate > delay {
				delay = candidate
			}
		} else if deadline, err := http.ParseTime(retryAfter); err == nil {
			if candidate := time.Until(deadline); candidate > delay {
				delay = candidate
			}
		}
	}
	for _, field := range []string{"X-Organization-Rate-Limit-Reset", "X-Instant-Test-Rate-Limit-Reset"} {
		if reset, err := strconv.ParseInt(headers.Get(field), 10, 64); err == nil {
			if candidate := time.Until(time.Unix(reset, 0)); candidate > delay {
				delay = candidate
			}
		}
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func pendingResultError(err error) bool {
	var status *statusError
	return errors.As(err, &status) && status.code == http.StatusNotFound
}
