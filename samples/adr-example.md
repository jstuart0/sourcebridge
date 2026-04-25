---
sourcebridge:
    page_id: sourcebridge.adr.a1b2c3d
    template: adr
    audience: for-engineers
    dependencies:
        dependency_scope: direct
---
<!-- sourcebridge:block id="b4867749a03ba" kind="heading" owner="generated" -->
# Replace legacy-jwt with golang-jwt v5
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bff60cdfc7be2" kind="paragraph" owner="generated" -->
**Date:** 2026-04-21 | **Author:** alice | **Commit:** `a1b2c3d`
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="ba465779ee2eb" kind="heading" owner="generated" -->
## Context
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b28c61f7bcb81" kind="paragraph" owner="generated" -->
The authentication library in use (legacy-jwt v2) had not received a security patch in over 3 years and carried 2 open CVEs (CVE-2024-1234, CVE-2024-5678). The team evaluated 3 replacement libraries against criteria of active maintenance, JWK rotation support, and minimal new dependencies. (a1b2c3d)
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bf7c844f578bf" kind="heading" owner="generated" -->
## Decision
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b75e61060c703" kind="paragraph" owner="generated" -->
We replaced legacy-jwt v2 with golang-jwt/jwt v5 (internal/auth/jwt.go:1-45). golang-jwt v5 is actively maintained, supports native JWK rotation, and introduces zero new transitive dependencies. The token algorithm was changed from RS256 to ES256 to align with the current key infrastructure. (a1b2c3d)
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b1565a535e812" kind="heading" owner="generated" -->
## Consequences
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b3f680bc5f1da" kind="paragraph" owner="generated" -->
All token validation paths now route through golang-jwt v5. The token format change invalidates existing sessions on first deployment — a one-time disruption that was accepted given the security posture improvement. The legacy parse path (internal/auth/legacy.go) is removed. Sessions older than the deploy window must re-authenticate. (a1b2c3d)
<!-- /sourcebridge:block -->

---

---
sourcebridge:
    page_id: sourcebridge.adr.e5f6007
    template: adr
    audience: for-engineers
    dependencies:
        dependency_scope: direct
---
<!-- sourcebridge:block id="b4cfe3b8ff23c" kind="heading" owner="generated" -->
# Feat: add job dispatcher to internal/jobs
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b85c1a2466dce" kind="paragraph" owner="generated" -->
**Date:** 2026-04-14 | **Author:** bob | **Commit:** `e5f6007`
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b34571aae09e5" kind="heading" owner="generated" -->
## Context
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bb096ffc02d52" kind="paragraph" owner="generated" -->
This week the team shipped the RequireRole middleware for route-level access control (internal/auth/role.go:18-44) and patched a nil-pointer panic that occurred when the JWT claim was absent. 3 commits, 7 files changed, net +76 lines.

No breaking changes this week. The panic fix was a 14-line targeted change covered by 2 new regression tests in internal/auth/role_test.go. The RequireRole middleware follows the same pattern as Middleware (internal/auth/middleware.go:10-35) and composes correctly with it. (e5f6007)
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bc7efa64d2a3e" kind="heading" owner="generated" -->
## Decision
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bc76bb0bfac68" kind="paragraph" owner="generated" -->
_No content detected in this section._
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b8cf84521f7c5" kind="heading" owner="generated" -->
## Consequences
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bff30cd877eda" kind="paragraph" owner="generated" -->
_No content detected in this section._
<!-- /sourcebridge:block -->

---

---
sourcebridge:
    page_id: sourcebridge.adr.f600123
    template: adr
    audience: for-engineers
    dependencies:
        dependency_scope: direct
---
<!-- sourcebridge:block id="b8ea3b3346b8c" kind="heading" owner="generated" -->
# Adopt event-driven architecture for order processing
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b717d2ae740ee" kind="paragraph" owner="generated" -->
**Date:** 2026-04-07 | **Author:** carol | **Commit:** `f600123`
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bfe622bedac1f" kind="heading" owner="generated" -->
## Context
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b0d1e76287932" kind="paragraph" owner="generated" -->
The synchronous RPC chain between the order, payment, and fulfilment services was causing cascading timeouts under sustained load (>500 rps). A single slow payment provider call was blocking order acknowledgement for 30+ seconds. (f600123)
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b5f8ac0dc5bfd" kind="heading" owner="generated" -->
## Decision
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b6b4ceafc8f3e" kind="paragraph" owner="generated" -->
We are introducing Kafka as an event bus between these three services. Orders emit an OrderPlaced event; payment and fulfilment subscribe independently (internal/events/order.go). This breaks the synchronous dependency chain and allows each service to scale independently. (f600123)
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="bd34efad8e58c" kind="heading" owner="generated" -->
## Consequences
<!-- /sourcebridge:block -->

<!-- sourcebridge:block id="b0acecbd97903" kind="paragraph" owner="generated" -->
Order status updates are now eventually consistent. We accept this trade-off in exchange for fault isolation — a payment service outage no longer stalls order acknowledgement. Consumers must be idempotent; this is enforced via the deduplication key on the Kafka consumer group. (f600123)
<!-- /sourcebridge:block -->
