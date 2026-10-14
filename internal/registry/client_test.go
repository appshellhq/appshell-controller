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

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// clientFor is a client for server, presenting the token at tokenPath over the server's TLS.
func clientFor(t *testing.T, server *httptest.Server, tokenPath string) *Client {
	t.Helper()
	c, err := NewClient(server.URL, tokenPath, false)
	if err != nil {
		t.Fatal(err)
	}

	return c.WithHTTPClient(server.Client())
}

var resource = Resource{
	APIVersion: APIVersion,
	Kind:       Kind,
	Name:       "acme/storefront",
	Spec:       Spec{Packages: &[]string{"acme/catalog@1.2.0"}},
}

func TestApplySendsTheResourceWithTheProjectedToken(t *testing.T) {
	var got struct {
		path, auth, contentType string
		body                    Resource
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth, got.contentType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"id":"acme/storefront","created":false,"changed":true,"changes":["activated acme/catalog@1.2.0"],"message":"Application updated"}`))
	}))
	defer server.Close()

	result, err := clientFor(t, server, writeToken(t, "the-token\n")).Apply(context.Background(), resource)
	if err != nil {
		t.Fatal(err)
	}

	if got.path != "/v1/apply" || got.auth != "Bearer the-token" || got.contentType != "application/json" {
		t.Errorf("sent %s with %q, %q", got.path, got.auth, got.contentType)
	}
	if got.body.Name != "acme/storefront" || got.body.Kind != Kind || got.body.APIVersion != APIVersion {
		t.Errorf("sent %+v", got.body)
	}
	if result.Changed == nil || !*result.Changed || len(result.Changes) != 1 {
		t.Errorf("answered %+v", result)
	}
}

// The kubelet rotates the token; a copy held in memory would outlive it.
func TestApplyReadsTheTokenAfreshEachTime(t *testing.T) {
	var auths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"id":"acme/storefront"}`))
	}))
	defer server.Close()

	path := writeToken(t, "first")
	client := clientFor(t, server, path)

	if _, err := client.Apply(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Apply(context.Background(), resource); err != nil {
		t.Fatal(err)
	}

	if strings.Join(auths, ",") != "Bearer first,Bearer rotated" {
		t.Errorf("presented %q", auths)
	}
}

// A redirect is a request to send the token somewhere else; it isn't followed, so nothing
// reaches the place it points to.
func TestApplyNeverFollowsARedirectWithTheToken(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer elsewhere.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/apply", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := clientFor(t, server, writeToken(t, "the-token")).Apply(context.Background(), resource)
	if err == nil {
		t.Fatal("a redirect was taken for success")
	}
	if _, refused := Refused(err); refused {
		t.Errorf("a redirect is worth retrying, not a refusal: %v", err)
	}
	if reached.Load() {
		t.Error("the redirect was followed")
	}
}

func TestApplyClassifiesWhatTheRegistrySays(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		refusal Refusal
		message string
	}{
		{http.StatusUnauthorized, `{"message":"The deployment token is not valid"}`, RefusalUnauthenticated, "The deployment token is not valid"},
		{http.StatusForbidden, `{"message":"No trusted deployer on acme/storefront matches this workload."}`, RefusalNotTrusted, "No trusted deployer"},
		{http.StatusBadRequest, `{"message":["each package must be scope/name@version"]}`, RefusalInvalid, "each package"},
		{http.StatusNotFound, `{"message":"Package acme/catalog@9.9.9 is not published."}`, RefusalInvalid, "not published"},
		// Worth retrying soon: replaced mid-apply, rate limited, or the registry's own trouble.
		{http.StatusConflict, `{"message":"acme/storefront was replaced while this was being applied"}`, "", "replaced"},
		{http.StatusTooManyRequests, ``, "", "429"},
		{http.StatusServiceUnavailable, `not json`, "", "503"},
	} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))

		_, err := clientFor(t, server, writeToken(t, "the-token")).Apply(context.Background(), resource)
		server.Close()

		refusal, refused := Refused(err)
		if refusal != tc.refusal || refused != (tc.refusal != "") {
			t.Errorf("%d: refusal %q (%v), want %q", tc.status, refusal, refused, tc.refusal)
		}
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%d: error %v, want it to mention %q", tc.status, err, tc.message)
		}
	}
}

func TestApplyCapsWhatItReads(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"` + strings.Repeat("x", maxResponseBytes) + `"}`))
	}))
	defer server.Close()

	_, err := clientFor(t, server, writeToken(t, "the-token")).Apply(context.Background(), resource)
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("error %v, want the answer refused as too large", err)
	}
}

// With no token, nothing is sent: an apply without the workload's identity can't succeed, and an
// unauthenticated request is noise in the registry's logs.
func TestApplySendsNothingWithoutAToken(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer server.Close()

	for _, path := range []string{filepath.Join(t.TempDir(), "absent"), writeToken(t, "  \n")} {
		if _, err := clientFor(t, server, path).Apply(context.Background(), resource); err == nil {
			t.Errorf("%s: applied without a token", path)
		}
	}
	if reached.Load() {
		t.Error("a request was sent without a token")
	}
}

func TestNewClientRefusesWhereTheTokenShouldNotGo(t *testing.T) {
	token := writeToken(t, "the-token")
	for _, tc := range []struct {
		url           string
		allowInsecure bool
	}{
		{"http://registry.example.com", false},
		{"registry.example.com", false},
		{"ftp://registry.example.com", true},
		{"https://user:pass@registry.example.com", false},
		{"https://registry.example.com?tenant=acme", false},
		{"https://registry.example.com#deploy", false},
	} {
		if _, err := NewClient(tc.url, token, tc.allowInsecure); err == nil {
			t.Errorf("%s (insecure %v): accepted", tc.url, tc.allowInsecure)
		}
	}

	if _, err := NewClient("https://registry.example.com", "", false); err == nil {
		t.Error("accepted no token path")
	}
	if _, err := NewClient("http://localhost:7070", token, true); err != nil {
		t.Errorf("refused plain http with allowInsecure: %v", err)
	}
}

// The rules hold whatever client a test supplies: no redirects, and the 30-second bound.
func TestWithHTTPClientKeepsTheRules(t *testing.T) {
	c, err := NewClient("https://registry.example.com", writeToken(t, "the-token"), false)
	if err != nil {
		t.Fatal(err)
	}
	c.WithHTTPClient(&http.Client{Timeout: time.Hour})

	if c.http.Timeout != requestTimeout {
		t.Errorf("timeout %s, want %s", c.http.Timeout, requestTimeout)
	}
	if c.http.CheckRedirect == nil {
		t.Error("the redirect rule was dropped")
	}
}

func TestErrorsAreErrors(t *testing.T) {
	if _, ok := errors.AsType[*Error](error(&Error{Status: 403, Refusal: RefusalNotTrusted})); !ok {
		t.Error("*Error is not an error")
	}
}
