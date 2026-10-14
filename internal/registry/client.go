/*
Copyright 2026 The Appshell Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package registry is the controller's client for the appshell registry's apply API.
//
// It holds the one credential the controller has, so it is deliberately narrow: one registry,
// fixed by the operator; one route; the workload's own projected token, read fresh for every
// request; no redirects followed, since a redirect could carry the token elsewhere.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// APIVersion and Kind are the registry's apply resource, which an AppshellApplication mirrors.
	APIVersion = "registry.appshell.org/v1"
	Kind       = "Application"

	// maxResponseBytes caps what is read from the registry: an apply's answer is small, and a
	// registry that sends more isn't answering an apply.
	maxResponseBytes = 1 << 20

	// requestTimeout bounds one apply, end to end.
	requestTimeout = 30 * time.Second
)

// Resource is the body of `POST /v1/apply`.
type Resource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Spec       Spec   `json:"spec"`
}

// Spec is the registry Application's desired state. Fields left nil are left as they are in the
// registry; Packages left nil leaves the application's packages alone, and an empty, non-nil
// Packages deactivates every one.
type Spec struct {
	Shell                 json.RawMessage `json:"shell,omitempty"`
	Packages              *[]string       `json:"packages,omitempty"`
	Overrides             json.RawMessage `json:"overrides,omitempty"`
	AllowOverrides        *bool           `json:"allowOverrides,omitempty"`
	AllowOverlays         *bool           `json:"allowOverlays,omitempty"`
	ServingBaseURL        *string         `json:"servingBaseUrl,omitempty"`
	SharedBaselines       json.RawMessage `json:"sharedBaselines,omitempty"`
	SharedDepsEnforcement *string         `json:"sharedDepsEnforcement,omitempty"`
	TokenEnforcement      *string         `json:"tokenEnforcement,omitempty"`
	VarsEnforcement       *string         `json:"varsEnforcement,omitempty"`
}

// Result is what the registry answers an apply with.
type Result struct {
	ID      string   `json:"id"`
	Created bool     `json:"created"`
	Changes []string `json:"changes"`
	Message string   `json:"message"`
	// Changed is false when the registry recorded nothing. Registries that predate it don't
	// send it; nil is read as changed.
	Changed *bool `json:"changed,omitempty"`
}

// Refusal says why the registry turned an apply away, as a stable reason a condition can carry.
type Refusal string

const (
	// RefusalUnauthenticated: the token was refused — not this registry's deploy audience,
	// expired, or from an issuer it doesn't accept.
	RefusalUnauthenticated Refusal = "Unauthenticated"
	// RefusalNotTrusted: no trusted-deployment record on the application matches this workload,
	// or the application doesn't exist; the registry answers both the same way.
	RefusalNotTrusted Refusal = "NotTrusted"
	// RefusalInvalid: the registry refused the resource itself — a package not published, a
	// field it doesn't accept.
	RefusalInvalid Refusal = "Invalid"
)

// Error is a failed apply. Refusal is set when the registry turned it away, which retrying won't
// change until something else does; it's empty for failures worth retrying soon.
type Error struct {
	Status  int
	Message string
	Refusal Refusal
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return e.Message
	}

	return fmt.Sprintf("registry answered %d: %s", e.Status, e.Message)
}

// Refused reports whether err is the registry turning an apply away, and why.
func Refused(err error) (Refusal, bool) {
	var e *Error
	if errors.As(err, &e) && e.Refusal != "" {
		return e.Refusal, true
	}

	return "", false
}

// Client applies resources to one registry, as the controller's workload.
type Client struct {
	applyURL  string
	tokenPath string
	http      *http.Client
}

// NewClient is a client for the registry at baseURL, presenting the token the kubelet projects at
// tokenPath. baseURL must be https unless allowInsecure, which is for local development only:
// the token is a credential.
func NewClient(baseURL, tokenPath string, allowInsecure bool) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("registry URL %q is not an absolute http(s) URL", baseURL)
	}
	if u.Scheme != "https" && !allowInsecure {
		return nil, fmt.Errorf("registry URL %q is not https; the controller won't send its token in the clear", baseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("registry URL %q must not carry credentials, a query or a fragment", baseURL)
	}
	if tokenPath == "" {
		return nil, errors.New("no token path: the controller authenticates with its projected service-account token")
	}

	return &Client{
		applyURL:  strings.TrimRight(u.String(), "/") + "/v1/apply",
		tokenPath: tokenPath,
		http: &http.Client{
			Timeout: requestTimeout,
			// Never followed: the request carries the controller's token, and a redirect is a
			// registry, or something in front of it, asking to send it somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// WithHTTPClient replaces the transport, keeping the redirect and timeout rules. For tests.
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	copied := *hc
	copied.CheckRedirect = c.http.CheckRedirect
	if copied.Timeout == 0 {
		copied.Timeout = c.http.Timeout
	}
	c.http = &copied

	return c
}

// token reads the projected token afresh: the kubelet rotates it well before it expires, and a
// copy held in memory would outlive it.
func (c *Client) token() (string, error) {
	raw, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", &Error{Message: fmt.Sprintf("reading the service-account token: %v", err)}
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", &Error{Message: fmt.Sprintf("the service-account token at %s is empty", c.tokenPath)}
	}

	return token, nil
}

// Apply reconciles the registry application named by r against it.
func (c *Client) Apply(ctx context.Context, r Resource) (Result, error) {
	token, err := c.token()
	if err != nil {
		return Result{}, err
	}

	body, err := json.Marshal(r)
	if err != nil {
		return Result{}, fmt.Errorf("encoding %s: %w", r.Name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.applyURL, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, &Error{Message: fmt.Sprintf("reaching the registry: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("reading the answer: %v", err)}
	}
	if len(raw) > maxResponseBytes {
		return Result{}, &Error{Status: resp.StatusCode, Message: "the answer is larger than an apply's"}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, &Error{
			Status:  resp.StatusCode,
			Message: messageOf(raw, resp.Status),
			Refusal: refusalOf(resp.StatusCode),
		}
	}

	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("decoding the answer: %v", err)}
	}

	return result, nil
}

/*
 * Which answers are the registry turning the apply away, which retrying won't change until a
 * person does something, and which are worth retrying soon: a conflict (the application was
 * replaced mid-apply), a redirect (refused, but the registry may be mid-reconfiguration), rate
 * limiting, and anything server-side.
 */
func refusalOf(status int) Refusal {
	switch status {
	case http.StatusUnauthorized:
		return RefusalUnauthenticated
	case http.StatusForbidden:
		return RefusalNotTrusted
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return RefusalInvalid
	default:
		return ""
	}
}

// messageOf is the registry's own reason when it gave one, as `{"message": ...}`, a string or a
// list of them; otherwise the status line.
func messageOf(raw []byte, status string) string {
	var body struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil && len(body.Message) > 0 {
		var one string
		if json.Unmarshal(body.Message, &one) == nil && one != "" {
			return one
		}
		var many []string
		if json.Unmarshal(body.Message, &many) == nil && len(many) > 0 {
			return strings.Join(many, "; ")
		}
	}

	return status
}
