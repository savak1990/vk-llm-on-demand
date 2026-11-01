# ADR 0001: GPU workers are external provider instances, never cluster nodes

## Status

Accepted (2026-11-01).

## Context

The control plane runs in a small Hetzner k3s cluster (one `cx23` control
plane, one to two `cx43` workers) that has no GPU and must stay cheap and
permanently available. The expensive resource, an NVIDIA L40S, is rented by
the hour from a marketplace (Vast.ai first). Marketplace instances are
unprivileged Docker containers on hosts we do not control, with random
external ports on a usually shared public IP, no TUN device, and no network
volumes ([research §1.6–1.7](../research.md)). The cluster has no stable
public IP either.

The brief asks for a Kubernetes-native control plane but explicitly does not
want the GPU machine to join the cluster.

## Decision

1. A GPU worker is a **Vast.ai Docker instance** created through the provider
   API, running the inference server image directly. It is not a Kubernetes
   node, not a pod, and holds no cluster credentials.
2. The control plane talks to the provider through one Go interface:

   ```go
   type Provider interface {
       SearchOffers(ctx context.Context, f OfferFilter) ([]Offer, error)
       CreateInstance(ctx context.Context, r CreateRequest) (InstanceID, error)
       GetInstance(ctx context.Context, id InstanceID) (Instance, error)
       ListInstances(ctx context.Context, labelPrefix string) ([]Instance, error)
       DestroyInstance(ctx context.Context, id InstanceID) error
       Balance(ctx context.Context) (Money, error)
   }
   ```

   Exactly two implementations exist in the MVP: `vastai` and an in-memory
   `fake` for tests. No registry, no plugin loading, no second cloud until a
   second cloud is specified.
3. Every instance the control plane creates carries the provider `label`
   `vkllm:<installation-id>:<generation>`. The label is the identity the
   reconcile loop uses to recognise its own instances (ADR 0003, 0004).
4. The worker runs the official `llama-server` CUDA image (ADR 0002) and is
   reached through the link chosen in ADR 0006.

## Alternatives considered

- **Join the GPU host to k3s as an agent node.** Needs a routable private
  network or VPN between Hetzner and the host. Vast containers are
  unprivileged and have no TUN device, so WireGuard and Tailscale kernel mode
  are out, and a k3s agent cannot run inside an unprivileged container.
  Rejected for the MVP; recorded as a future option for providers that sell
  VMs.
- **Vast.ai Serverless** (worker groups with built-in scale-to-zero and an
  OpenAI facade). Its OpenAI proxy supports vLLM endpoints only, there is no
  llama.cpp template, and auth, budget and cold-start policy would be theirs,
  not ours ([research §1.8](../research.md)). Rejected as the product, kept
  as the comparison point in `overview.md`.
- **A generic multi-cloud abstraction now** (Runpod, Civo, Vast behind one
  rich interface). One implementation does not justify it; the six-method
  interface above is the whole abstraction until a second provider exists.

## Rationale

The cheapest correct boundary is the provider API itself. Treating the GPU
as a disposable external process keeps the cluster unchanged, keeps the
blast radius of a compromised worker to one API key with no cluster reach,
and lets the whole lifecycle be expressed as create / observe / destroy.

## Consequences

- Kubernetes scheduling, probes and NetworkPolicy do not apply to the worker.
  The controller owns readiness, health and teardown itself (ADR 0003).
- Connectivity to the worker is an explicit design problem (ADR 0006).
- Observability of the worker is pulled through the control plane
  (`/metrics` over the worker link), not scraped by Prometheus directly.
- Adding Runpod or Civo later means a second `Provider` implementation plus a
  provider-specific offer filter, nothing else, provided the label and
  readiness contracts are kept.

## Risks

- Marketplace variability: a host can be slow, lie about bandwidth, or go
  offline. Mitigated by offer filters, startup deadlines, and the destroy-and-
  retry rule in ADR 0003.
- A worker whose control plane disappears keeps billing. Mitigated by the
  worker-side dead-man timer (ADR 0003, spec 070).
