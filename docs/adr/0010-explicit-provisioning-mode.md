# ADR 0010: Explicit provisioning is the MVP default; on-demand provisioning is a future mode

## Status

Accepted (2026-11-01). Supersedes [ADR 0007](0007-cold-start-api-strategy.md)
as the MVP behaviour. ADR 0007 remains the design of the future
`on-demand` mode.

## Context

ADR 0007 made the first inference request rent the GPU and hid the cold
start behind a bounded wait with SSE heartbeats. The updated brief changes
the product shape: the user runs `vk-llm start`, works with an ordinary
inference client such as OpenCode, and runs `vk-llm stop` or lets the
30-minute inactivity timeout (ADR 0011) release the GPU. Inference requests
must not rent GPUs on their own in the MVP, and the client must need no
fork, wrapper, heartbeat or lease.

The brief also re-states PostgreSQL as a preferred state store. The state
the lifecycle needs (desired state, observed state, provider id, deadlines,
last inference time, retry metadata) fits the ConfigMap document of
ADR 0004 unchanged in kind; that decision stands, with the fields extended
by ADR 0011.

## Decision

1. **Configuration:** `lifecycle.provisioningMode` is `explicit` (default)
   or `on-demand`. The MVP implements `explicit` only; `on-demand` is parsed,
   rejected with a clear startup error until implemented, and designed in
   ADR 0007.
2. **Explicit mode.** The GPU is started by `POST /api/v1/gpu/start`
   (ADR 0012), never by an inference request. An inference request that
   arrives with no Ready backend returns:

   ```http
   HTTP/1.1 503 Service Unavailable
   Content-Type: application/json

   {"error": {"type": "server_error", "code": "backend_unavailable",
              "message": "GPU backend is stopped; start it with `vk-llm start`"}}
   ```

   No `Retry-After` header is sent, so SDK clients fail fast instead of
   retrying for minutes. `GET /v1/models` always answers from
   configuration. Nothing on the inference path registers demand,
   touches the inactivity deadline, or changes desired state.
3. **Draining and stopped.** A request arriving while the state is
   `Draining`, `Deleting` or `Stopped`, or while `desired` is `STOPPED`,
   gets the same `backend_unavailable` answer. Admission and the shutdown
   transition are serialized (ADR 0011 §4).
4. **On-demand mode (future).** When enabled, an inference request
   registers demand and waits as ADR 0007 describes (bounded wait,
   heartbeats, `gpu_cold_start`). The `Demand` controller method and the
   `onDemand.*` configuration block (`maxWait`, `maxPending`,
   `heartbeatInterval`) exist only for that mode and are not part of the
   MVP code paths.
5. **Workflow leases are removed** from the design (ADR 0011 §5). The
   rolling inactivity timeout replaces them.

## Alternatives considered

- **Keep on-demand as default** (ADR 0007). Correct for unattended
  clients, but it couples spend to any authenticated request, makes the
  first request of a session slow, and the brief explicitly moves the
  decision to the user.
- **Explicit start plus an automatic start on the first request**
  (hybrid). Two triggers for one expensive action; rejected for the MVP.
- **Return 202 with a polling URL.** Not OpenAI-compatible; no client would
  use it.

## Rationale

Explicit mode makes GPU spend an intentional act, keeps the inference
gateway trivially simple while the GPU is stopped, and needs nothing from
the client. The on-demand design is preserved for later without blocking
the MVP.

## Consequences

- The gateway has no pending queue, no heartbeat loop and no cold-start
  wait in the MVP; spec 060 shrinks. The Envoy route timeout becomes
  `model.generationTimeout + 60 s` (660 s) or disabled; the 1080 s figure
  of ADR 0007 §4 applies only to on-demand mode (spec 130).
- A management API, a CLI and a GitHub Actions workflow become the ways to
  start a GPU (ADR 0012).
- The metrics `vkllm_gateway_pending_requests`, `vkllm_gateway_wait_seconds`
  and `vkllm_gateway_cold_start_outcomes_total` stay in the catalog and
  read zero in explicit mode.

## Risks

- Users forget to start the GPU and see 503s. Mitigation: the error
  message names the command; the README's OpenCode section shows the
  three-command workflow.
- A future on-demand implementation drifts from ADR 0007. Mitigation: the
  ADR stays Accepted for that mode and spec 060 keeps a "future: on-demand"
  section.
