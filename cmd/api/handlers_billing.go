// Self-serve plan upgrades, backed by Dodo Payments' hosted checkout
// (https://dodopayments.com/). This deliberately does not build any
// payment-collection UI of our own: handleCreateBillingCheckout only ever
// hands the console a URL to redirect the browser to, and Dodo tells us the
// outcome asynchronously over a signed webhook (handleDodoWebhook) rather
// than a client ever being trusted to say "I paid, upgrade me."
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dodopayments/dodopayments-go"
	"github.com/dodopayments/dodopayments-go/shared"

	"github.com/darkoatanasovski/chat/internal/quota"
)

// upgradableTiers is the subset of validTiers a checkout may purchase: FREE
// isn't something you buy, and ENTERPRISE has no fixed price to check out
// with (that's a contact-sales conversation, same as today).
var upgradableTiers = map[string]bool{
	quota.TierPro:      true,
	quota.TierBusiness: true,
}

// tierRank orders tiers strictly for the "can only self-serve upgrade,
// never downgrade" rule below. A downgrade (e.g. BUSINESS -> PRO) would
// need the org's existing Dodo subscription cancelled first — this
// checkout flow only ever starts a brand-new one — so it's rejected here
// rather than silently leaving the org paying for two subscriptions.
var tierRank = map[string]int{
	quota.TierFree:       0,
	quota.TierPro:        1,
	quota.TierBusiness:   2,
	quota.TierEnterprise: 3,
}

type billingCheckoutRequest struct {
	Tier string `json:"tier"`
}

type billingCheckoutResponse struct {
	CheckoutURL string `json:"checkout_url"`
}

// handleCreateBillingCheckout mints a Dodo Payments checkout session for
// upgrading the calling org to a paid tier and hands back its hosted
// checkout_url. The org's tier does not change here — only once
// handleDodoWebhook sees the resulting subscription go active.
func (a *App) handleCreateBillingCheckout(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	orgUser, _ := orgUserIdentityFromContext(r.Context())

	var req billingCheckoutRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Tier = strings.ToUpper(strings.TrimSpace(req.Tier))
	if !upgradableTiers[req.Tier] {
		writeError(w, http.StatusBadRequest, "tier must be one of PRO, BUSINESS")
		return
	}
	productID := a.cfg.DodoProductIDs[req.Tier]
	if a.cfg.DodoAPIKey == "" || productID == "" {
		writeError(w, http.StatusServiceUnavailable, "billing is not configured")
		return
	}

	org, err := a.orgsRepo.Get(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start checkout")
		return
	}
	if org.Tier == req.Tier {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("organization is already on the %s plan", req.Tier))
		return
	}
	if tierRank[req.Tier] < tierRank[org.Tier] {
		writeError(w, http.StatusBadRequest, "self-serve checkout can only upgrade to a higher plan")
		return
	}

	user, err := a.orgUsersRepo.GetByID(r.Context(), orgUser.UserID)
	if err != nil {
		a.log.Error("load org user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start checkout")
		return
	}

	// Reuse the Dodo customer record from a previous checkout (any tier)
	// instead of minting a new one every time this org buys or changes a
	// plan.
	var customer dodopayments.CustomerRequestUnionParam
	if org.DodoCustomerID != "" {
		customer = dodopayments.AttachExistingCustomerParam{CustomerID: dodopayments.F(org.DodoCustomerID)}
	} else {
		customer = dodopayments.NewCustomerParam{Email: dodopayments.F(user.Email)}
	}

	session, err := a.dodo.CheckoutSessions.New(r.Context(), dodopayments.CheckoutSessionNewParams{
		CheckoutSessionRequest: dodopayments.CheckoutSessionRequestParam{
			ProductCart: dodopayments.F([]dodopayments.ProductItemReqParam{{
				ProductID: dodopayments.F(productID),
				Quantity:  dodopayments.F(int64(1)),
			}}),
			Customer:  dodopayments.F(customer),
			ReturnURL: dodopayments.F(a.cfg.ConsoleBaseURL + "/console/billing/return"),
			// handleDodoWebhook reads org_id/tier back out of the
			// resulting subscription's metadata (Dodo carries checkout
			// metadata forward onto the subscription/payment it creates)
			// to know which org to upgrade and to what — never trusting
			// the webhook payload's business/customer id alone, since
			// that's Dodo's identifier, not ours.
			Metadata: dodopayments.F(dodopayments.MetadataParam{
				"org_id": shared.UnionString(strconv.FormatInt(orgIdentity.OrgID, 10)),
				"tier":   shared.UnionString(req.Tier),
			}),
		},
	})
	if err != nil {
		a.log.Error("create dodo checkout session", "error", err)
		writeError(w, http.StatusBadGateway, "failed to start checkout")
		return
	}
	if session.CheckoutURL == "" {
		a.log.Error("dodo checkout session has no checkout_url", "session_id", session.SessionID)
		writeError(w, http.StatusBadGateway, "failed to start checkout")
		return
	}

	writeJSON(w, http.StatusCreated, billingCheckoutResponse{CheckoutURL: session.CheckoutURL})
}

// handleDodoWebhook is Dodo Payments calling back into this platform, never
// a browser client — it sits outside every dashboard auth middleware.
// Authenticity comes entirely from the webhook signature
// (client.Webhooks.Unwrap, checked against cfg.DodoWebhookKey baked into
// a.dodo at construction), not from any bearer token.
func (a *App) handleDodoWebhook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	event, err := a.dodo.Webhooks.Unwrap(body, r.Header)
	if err != nil {
		a.log.Warn("dodo webhook signature verification failed", "error", err)
		writeError(w, http.StatusBadRequest, "invalid webhook signature")
		return
	}

	switch event.Type {
	case dodopayments.UnwrapWebhookEventTypeSubscriptionActive:
		a.handleDodoSubscriptionActive(r.Context(), event)
	case dodopayments.UnwrapWebhookEventTypeSubscriptionCancelled,
		dodopayments.UnwrapWebhookEventTypeSubscriptionExpired,
		dodopayments.UnwrapWebhookEventTypeSubscriptionFailed:
		a.handleDodoSubscriptionEnded(r.Context(), event)
	default:
		// Renewals, payment attempts, disputes, etc. don't change which
		// tier an org is on — nothing for this handler to do with them.
	}

	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func (a *App) handleDodoSubscriptionActive(ctx context.Context, event *dodopayments.UnwrapWebhookEvent) {
	sub, ok := event.Data.(dodopayments.Subscription)
	if !ok {
		a.log.Error("dodo subscription.active event had unexpected data shape")
		return
	}
	orgID, tier, ok := dodoBillingMetadata(sub.Metadata)
	if !ok {
		a.log.Error("dodo subscription.active event missing org_id/tier metadata", "subscription_id", sub.SubscriptionID)
		return
	}
	if err := a.orgsRepo.UpgradeTier(ctx, orgID, tier, sub.Customer.CustomerID, sub.SubscriptionID); err != nil {
		a.log.Error("upgrade organization tier", "error", err, "org_id", orgID, "tier", tier)
	}
}

// handleDodoSubscriptionEnded reverts an org to FREE once its paid
// subscription stops being active for any reason. organizations.DowngradeTier
// only applies this if dodoSubscriptionID still matches the subscription on
// file — see its doc comment for why that guard exists.
func (a *App) handleDodoSubscriptionEnded(ctx context.Context, event *dodopayments.UnwrapWebhookEvent) {
	sub, ok := event.Data.(dodopayments.Subscription)
	if !ok {
		a.log.Error("dodo subscription-ended event had unexpected data shape")
		return
	}
	orgID, _, ok := dodoBillingMetadata(sub.Metadata)
	if !ok {
		a.log.Error("dodo subscription-ended event missing org_id metadata", "subscription_id", sub.SubscriptionID)
		return
	}
	if err := a.orgsRepo.DowngradeTier(ctx, orgID, sub.SubscriptionID, quota.TierFree); err != nil {
		a.log.Error("downgrade organization tier", "error", err, "org_id", orgID)
	}
}

// dodoBillingMetadata extracts the org_id/tier metadata this platform
// attaches to every checkout session it creates (see
// handleCreateBillingCheckout) back out of a Dodo webhook payload.
func dodoBillingMetadata(md dodopayments.Metadata) (orgID int64, tier string, ok bool) {
	orgIDVal, hasOrgID := md["org_id"].(shared.UnionString)
	tierVal, hasTier := md["tier"].(shared.UnionString)
	if !hasOrgID || !hasTier {
		return 0, "", false
	}
	id, err := strconv.ParseInt(string(orgIDVal), 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, string(tierVal), true
}
