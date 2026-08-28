package main

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestHandleCreateOrg_Valid(t *testing.T) {
	app := testApp(t)
	var resp orgResponse
	rec := do(t, app, jsonRequest("POST", "/organizations", createOrgRequest{Name: "acme-" + uuid.NewString(), Tier: "PRO"}), &resp)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.Tier != "PRO" {
		t.Fatalf("expected tier PRO, got %q", resp.Tier)
	}
	if resp.Token == "" {
		t.Fatalf("expected a non-empty org-admin token")
	}
	if resp.OrgID == 0 {
		t.Fatalf("expected a non-zero numeric org_id")
	}
}

func TestHandleCreateOrg_DefaultsToFreeTierWhenUnspecified(t *testing.T) {
	app := testApp(t)
	var resp orgResponse
	rec := do(t, app, jsonRequest("POST", "/organizations", createOrgRequest{Name: "no-tier-" + uuid.NewString()}), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.Tier != "FREE" {
		t.Fatalf("expected default tier FREE when unspecified, got %q", resp.Tier)
	}
}

func TestHandleCreateOrg_LowercaseTierIsNormalized(t *testing.T) {
	app := testApp(t)
	var resp orgResponse
	rec := do(t, app, jsonRequest("POST", "/organizations", createOrgRequest{Name: "lowercase-" + uuid.NewString(), Tier: "business"}), &resp)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resp.Tier != "BUSINESS" {
		t.Fatalf("expected lowercase tier to normalize to BUSINESS, got %q", resp.Tier)
	}
}

func TestHandleCreateOrg_Validation(t *testing.T) {
	app := testApp(t)
	cases := []struct {
		name string
		req  createOrgRequest
	}{
		{"empty name", createOrgRequest{Name: "  ", Tier: "FREE"}},
		{"name too long", createOrgRequest{Name: string(make([]byte, 129)), Tier: "FREE"}},
		{"invalid tier", createOrgRequest{Name: "bad-tier-org", Tier: "PLATINUM"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, app, jsonRequest("POST", "/organizations", tc.req), nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
