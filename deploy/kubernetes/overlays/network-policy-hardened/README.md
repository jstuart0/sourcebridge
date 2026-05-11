# NetworkPolicy Hardening Overlay

This overlay adds a default-deny-all posture plus an explicit allow-set for all
SourceBridge pods. It is **entirely opt-in** — the base manifest is not modified
and `kubectl apply -k deploy/kubernetes/base/` continues to behave exactly as
before.

## What this does

Applies two additional resource files on top of the base:

1. `default-deny.yaml` — a single NetworkPolicy selecting all pods (`podSelector:
   {}`) that blocks both Ingress and Egress by default.
2. `allow-set.yaml` — per-component allow policies that carve out exactly the
   traffic each component requires.

The full allow-set covers:
- DNS egress (UDP+TCP :53 → kube-dns in kube-system) for all pods
- API ingress from Traefik ingress controller and web pods on :8080
- API egress to SurrealDB:8000, Redis:6379, worker:50051, external :443/:80/:22
- Worker ingress from API on :50051 (gRPC)
- Worker egress to SurrealDB:8000, external :443/:80
- Web ingress from Traefik ingress controller on :3000
- Web egress to API:8080, external :443
- SurrealDB ingress from API and worker on :8000
- Redis ingress from API on :6379

## Apply

```bash
kubectl apply -k deploy/kubernetes/overlays/network-policy-hardened/
```

## Rollback

```bash
kubectl delete -k deploy/kubernetes/overlays/network-policy-hardened/
```

The base policies (including the existing `worker-allow-api-only` policy) remain
in place after rollback.

## Before applying — verify your cluster topology

This overlay makes assumptions about your cluster that you MUST verify before
applying to a production cluster:

1. **Ingress controller label**: The allow policies target Traefik pods via
   `app.kubernetes.io/name: traefik`. If your cluster uses nginx-ingress,
   change the `podSelector` in `allow-api-ingress` and `allow-web-ingress` to:
   ```yaml
   app.kubernetes.io/name: ingress-nginx
   ```
   Also update the `namespaceSelector` to match the namespace where your
   ingress controller runs.

2. **kube-dns label**: The DNS egress policy targets `k8s-app: kube-dns`. Most
   clusters (kubeadm, GKE, EKS, AKS) use this label. Verify with:
   ```bash
   kubectl get pods -n kube-system -l k8s-app=kube-dns
   ```

3. **Ollama endpoint**: If your Ollama instance is cluster-internal (e.g. running
   in another namespace or as a sidecar), narrow the HTTP :80 egress in
   `allow-worker-egress` from the broad "all destinations" rule to a targeted
   `podSelector` or `namespaceSelector`.

4. **External LLM ports**: If you use a custom port for an LLM provider, add a
   matching egress rule to `allow-api-egress` and/or `allow-worker-egress`.

5. **Custom SurrealDB or Redis ports**: If you run SurrealDB or Redis on
   non-standard ports, update the relevant allow policies accordingly.

## Dry-run before applying

Always run a server-side dry-run against your actual cluster before applying:

```bash
kubectl apply --dry-run=server -k deploy/kubernetes/overlays/network-policy-hardened/
```

This catches label mismatches and API server validation errors without affecting
live traffic.

## Combining with the hardened security context overlay

Both overlays can be applied independently or together. If you want both
NetworkPolicy hardening and `readOnlyRootFilesystem: true`:

```bash
kubectl apply -k deploy/kubernetes/overlays/network-policy-hardened/
kubectl apply -k deploy/kubernetes/overlays/hardened/
```

There is no combined overlay — apply them individually so each can be rolled
back independently.
