-- Self-serve plan upgrades via Dodo Payments (hosted checkout). An org's
-- tier can now change after signup, not just at creation, so we need a
-- durable link back to Dodo's side of the relationship:
--
-- dodo_customer_id lets a repeat checkout attach to the same Dodo customer
-- instead of minting a new one every time an org upgrades or changes plan.
--
-- dodo_subscription_id is the currently-active subscription backing the
-- org's paid tier. It guards the downgrade path: a subscription.cancelled
-- webhook only takes effect if it names the subscription this org is
-- still on, so a stale cancellation for a since-replaced subscription
-- (e.g. PRO -> BUSINESS re-checkout) can't downgrade a currently-paying
-- org. See cmd/api/handlers_billing.go.
ALTER TABLE organizations ADD COLUMN dodo_customer_id     TEXT;
ALTER TABLE organizations ADD COLUMN dodo_subscription_id TEXT;
