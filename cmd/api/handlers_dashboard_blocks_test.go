package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestDashboardBlocks_CreateListAndUnblock(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Blocks Org")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	userA := createDashboardEndUser(t, app, ownerToken, appID, "Dash Block A")
	userB := createDashboardEndUser(t, app, ownerToken, appID, "Dash Block B")

	var created dashboardBlockResponse
	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/blocks", appID), dashboardBlockRequest{
		BlockerUserID: userA, BlockedUserID: userB,
	}), ownerToken), &created)
	if rec.Code != http.StatusCreated {
		t.Fatalf("dashboard block: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if created.BlockerUserID != userA || created.BlockedUserID != userB {
		t.Fatalf("unexpected block response: %+v", created)
	}

	var list []dashboardBlockListEntry
	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/blocks", appID), nil), ownerToken), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].BlockerUserID != userA || list[0].BlockedUserID != userB {
		t.Fatalf("list blocks: status = %d, list = %+v", rec.Code, list)
	}

	// The operator override: unblocking doesn't require being the blocker,
	// unlike the self-service DELETE /blocks/{user_id}.
	rec = do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/dashboard/apps/%d/blocks/%s/%s", appID, userA, userB), nil), ownerToken), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dashboard unblock: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/blocks", appID), nil), ownerToken), &list)
	if rec.Code != http.StatusOK || len(list) != 0 {
		t.Fatalf("expected no blocks after dashboard unblock, got %+v", list)
	}
}

func TestDashboardBlocks_UnblockNotFound(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Blocks Org 404")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	userA := createDashboardEndUser(t, app, ownerToken, appID, "No Block A")
	userB := createDashboardEndUser(t, app, ownerToken, appID, "No Block B")

	rec := do(t, app, authed(jsonRequest("DELETE", fmt.Sprintf("/dashboard/apps/%d/blocks/%s/%s", appID, userA, userB), nil), ownerToken), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unblock nonexistent block: status = %d, want 404", rec.Code)
	}
}

func TestDashboardBlocks_BothUsersMustBelongToApp(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Blocks Org Mismatch")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)
	userA := createDashboardEndUser(t, app, ownerToken, appID, "Mismatch A")

	otherOwnerToken, otherOrgID, _ := signUpDashboardOrg(t, app, "Other Org")
	otherAppID, _, _ := createTestApp(t, app, otherOrgID, otherOwnerToken)
	userFromOtherApp := createDashboardEndUser(t, app, otherOwnerToken, otherAppID, "Other App User")

	rec := do(t, app, authed(jsonRequest("POST", fmt.Sprintf("/dashboard/apps/%d/blocks", appID), dashboardBlockRequest{
		BlockerUserID: userA, BlockedUserID: userFromOtherApp,
	}), ownerToken), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("block across apps: status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardBlocks_CrossOrgIsolation(t *testing.T) {
	app := testApp(t)
	ownerToken, orgID, _ := signUpDashboardOrg(t, app, "Blocks Owner Org")
	appID, _, _ := createTestApp(t, app, orgID, ownerToken)

	otherToken, _, _ := signUpDashboardOrg(t, app, "Blocks Intruder Org")
	rec := do(t, app, authed(jsonRequest("GET", fmt.Sprintf("/dashboard/apps/%d/blocks", appID), nil), otherToken), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-org list blocks: status = %d, want 403", rec.Code)
	}
}
