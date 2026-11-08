package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryRetainsIdempotencyKey(t *testing.T) {
	var key string
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		if key == "" {
			key = r.Header.Get("Idempotency-Key")
		}
		if key == "" || key != r.Header.Get("Idempotency-Key") {
			t.Error("unstable idempotency key")
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"id":"bot_example"}`))
	}))
	defer s.Close()
	c := New(Config{Token: "secret", OrganizationID: "org_test", Endpoints: map[string]string{"orgs": s.URL}})
	resp, err := c.Do(context.Background(), "orgs", "POST", "/bots", map[string]string{"name": "test"})
	if err != nil || resp.StatusCode != 200 || calls != 2 {
		t.Fatalf("response=%v err=%v calls=%d", resp, err, calls)
	}
}

func TestRedirectNeverForwardsCredential(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer s.Close()
	c := New(Config{Token: "secret", Endpoints: map[string]string{"orgs": s.URL}})
	if _, err := c.Do(context.Background(), "orgs", "GET", "/", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 0 {
		t.Fatal("redirect followed")
	}
}

func TestListPaginatesAndEscapesCursor(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") == "" {
			w.Write([]byte(`{"items":[{"id":"one"}],"nextCursor":"a+b/="}`))
			return
		}
		if r.URL.Query().Get("cursor") != "a+b/=" {
			t.Error("cursor corrupted")
		}
		w.Write([]byte(`{"items":[{"id":"two"}],"nextCursor":null}`))
	}))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	rows, err := c.List(context.Background(), "orgs", "/bots")
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
}

func TestErrorsDoNotExposeResponseBodies(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req_123")
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "access_denied", "message": "sensitive secret"}})
	}))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	_, err := c.Do(context.Background(), "orgs", "GET", "/", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "access_denied" || apiErr.RequestID != "req_123" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "sensitive secret") {
		t.Fatal("untrusted response message exposed")
	}
}

func TestWaitRejectsForeignLocationsAndHonorsCancellation(t *testing.T) {
	c := New(Config{Endpoints: map[string]string{"compute": "https://compute-api.ezghcloud.com"}})
	if err := c.Wait(context.Background(), "compute", "https://other.example/operation"); err == nil {
		t.Fatal("foreign origin accepted")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Write([]byte(`{"operation":{"id":"op_test","status":"running"}}`))
	}))
	defer s.Close()
	c = New(Config{Endpoints: map[string]string{"compute": s.URL}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Wait(ctx, "compute", "/operations/op_test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
}

func TestPathsEscapeEveryIdentifier(t *testing.T) {
	if got := ProjectPath("org/a", "prj b", "vpcs", "vpc/c"); got != "/v1/organizations/org%2Fa/projects/prj%20b/vpcs/vpc%2Fc" {
		t.Fatal(got)
	}
}

func TestListReadsNamedAPICollections(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"bots":[{"id":"one"}],"nextCursor":null}`))
	}))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	rows, err := c.List(context.Background(), "orgs", "/bots")
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
}

func TestLongRetryAfterReturnsWithoutUnboundedWait(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Do(ctx, "orgs", "GET", "/bots", nil)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func TestListRejectsMissingPaginationMetadata(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) }))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	if _, err := c.List(context.Background(), "orgs", "/bots"); err == nil {
		t.Fatal("missing cursor could truncate an authoritative list")
	}
}

func TestAccessDeniedNamesRequiredPermissionWithoutEchoingMessage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"code":"access_denied","message":"Not allowed to bots.create; secret-token"}}`))
	}))
	defer s.Close()
	c := New(Config{Endpoints: map[string]string{"orgs": s.URL}})
	_, err := c.Do(context.Background(), "orgs", "GET", "/bots", nil)
	if err == nil || !strings.Contains(err.Error(), "bots.create") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("%v", err)
	}
}

func TestTransportErrorRetriesPostWithSameKey(t *testing.T) {
	var mu sync.Mutex // the hijacked call gives the client no happens-before edge
	var keys []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		switch n {
		case 1:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(409)
			w.Write([]byte(`{"error":{"code":"idempotency_in_progress","message":"x"}}`))
		default:
			w.WriteHeader(201)
		}
	}))
	defer s.Close()
	c := New(Config{Token: "secret", Endpoints: map[string]string{"compute": s.URL}})
	resp, err := c.Do(context.Background(), "compute", "POST", "/instances", map[string]string{"name": "a"})
	mu.Lock()
	defer mu.Unlock()
	if err != nil || resp.StatusCode != 201 || len(keys) != 3 {
		t.Fatalf("response=%v err=%v calls=%d", resp, err, len(keys))
	}
	if keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("idempotency keys differ: %q", keys)
	}
}

func TestTransportErrorNeverResendsKeylessPost(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("key sent")
		}
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer s.Close()
	c := New(Config{Token: "secret", Endpoints: map[string]string{"compute": s.URL}})
	_, err := c.DoWithHeaders(context.Background(), "compute", "POST", "/instances/i/actions", nil, http.Header{"Idempotency-Key": nil})
	if err == nil || !strings.Contains(err.Error(), "refresh state") || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
