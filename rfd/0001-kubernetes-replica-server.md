# RFD 0001 - Kubernetes Replica Server

- **Status:** Draft
- **Target:** Level 4
- **Author:** Erik Turner
- **Required Approvers:** Adam Carheden, Hugo Shaka, Roman Tkachenko

## What

Build a small Go service that manages replica counts for Kubernetes Deployments in one configured namespace.

The Level 4 implementation will provide:

- an HTTP API to list Deployments
- an HTTP API to read a Deployment replica count
- an HTTP API to update a Deployment replica count
- a health endpoint that verifies Kubernetes API connectivity
- a local cache maintained by watching Deployments
- mTLS between API callers and the server
- a Docker image
- a configurable Helm chart containing a Deployment, ServiceAccount, Role, RoleBinding, and Service
- Make targets to build, deploy, and run integration tests against KIND

## Why

The challenge is primarily an exercise in Kubernetes API interaction, API design, security, testing, packaging, and developer experience. The implementation should demonstrate those concepts without introducing unnecessary infrastructure.

The design therefore favors:

- standard library HTTP rather than a web framework
- `client-go` rather than an abstraction layer
- a shared informer rather than a custom cache
- the Deployment `/scale` subresource for writes
- namespace-scoped RBAC rather than cluster-wide access
- locally generated proof-of-concept certificates rather than requiring an external PKI product
- KIND and Helm for a repeatable reviewer workflow

## Scope

### In scope

Level 4 requirements are implemented.

The server manages Deployments only in its configured namespace. When deployed through the Helm chart, the namespace is automatically set to the pod's namespace using the Kubernetes Downward API. Replica values are restricted by the API to `0..100` as a proof-of-concept guard against accidental extreme scaling.

### Out of scope

- multi-cluster management
- cross-namespace management
- authentication/authorization beyond mTLS client identity verification
- persistent application storage
- certificate rotation while the process is running
- HPA coordination


## API structure

### `GET /api/v1/deployments`

Returns Deployments visible in the configured namespace and their cached desired replica counts.

Example:

```json
[
  {"name":"api","replicas":3},
  {"name":"worker","replicas":2}
]
```

### `GET /api/v1/deployments/{name}/replicas`

Returns the cached desired replica count.

```json
{"name":"api","replicas":3}
```

### `PUT /api/v1/deployments/{name}/replicas`

Request:

```json
{"replicas":5}
```

Response:

```json
{"name":"api","replicas":5}
```

The server updates only the Deployment `/scale` subresource rather than replacing the Deployment object.

### Health checks

The server exposes separate liveness and readiness endpoints on the internal health listener. These endpoints are intended for Kubernetes probes and are not exposed by the public Service.

#### `GET /livez`

Verifies that the server process is running and able to serve requests.

The liveness check does not make a Kubernetes API request. A temporary Kubernetes API outage should not cause the container to restart because restarting the process does not resolve an external dependency failure.

A successful check returns `200 OK`.

#### `GET /readyz`

Verifies that the server is ready to serve API traffic.

The readiness check verifies that:

- the Deployment informer has completed its initial cache synchronization; and
- the server can communicate with the Kubernetes API using its configured credentials.

A successful check returns `200 OK`. If the server is not ready, it returns `503 Service Unavailable`.

If Kubernetes connectivity is temporarily lost, the pod becomes unready and is removed from Service endpoints, but the process remains running. This allows the informer and Kubernetes client to recover without an unnecessary container restart.

### HTTP Status Codes

API endpoints use the following status codes:

- `400 Bad Request` - malformed input or invalid replica count
- `403 Forbidden` - Kubernetes authorization failure
- `404 Not Found` - Deployment not found
- `502 Bad Gateway` - Kubernetes API operation failed for another reason
- `503 Service Unavailable` - readiness check failed

Health endpoints use:

- `200 OK` - the corresponding health condition is satisfied
- `503 Service Unavailable` - the corresponding health condition is not satisfied

## Pod lifecycle

At startup the process:

1. loads in-cluster Kubernetes configuration when running as a pod (falling back to the developer's local kubeconfig when running the server outside the cluster)
2. creates a namespace-scoped shared Deployment informer
3. starts an internal health listener for kubelet probes
4. starts the informer
5. waits for initial cache synchronization
6. loads server certificate/key and client CA
7. starts the mTLS API listener


The process does not declare itself ready until the Deployment informer has completed its initial cache synchronization and Kubernetes API connectivity has been verified. 

Temporary loss of Kubernetes connectivity causes the readiness probe to fail but does not fail the liveness probe.

On `SIGTERM` or `SIGINT`, both HTTP servers are gracefully shut down with a bounded timeout. The informer stops through the shared context.

## Cache and consistency

A `client-go` shared informer performs an initial Deployment list and establishes a watch. The informer maintains the local cache from Kubernetes watch events, while `client-go` handles watch reconnection and re-listing as needed. Periodic resync is disabled because the service uses the informer as a read cache and does not require periodic reprocessing of cached objects.

Both read-only Deployment endpoints use the informer store. A normal GET therefore does not generate a Kubernetes API request.

Writes remain live Kubernetes API operations. After a successful `/scale` update, the API returns the Kubernetes response. The cached value is updated asynchronously by the Deployment watch. This creates a small eventual-consistency window after writes.

The API does not manually mutate the informer store after a write because doing so would create a second source of cache truth and could temporarily hide a conflicting Kubernetes update.

## TLS configuration

The public API listener uses mutual TLS:

- TLS 1.3 minimum
- server certificate authentication
- required client certificate
- client certificate verification against a configured CA

For the proof of concept, `make certs` uses a small Go utility to generate a 24-hour local CA, server certificate, and client certificate. Server material is loaded into Kubernetes as a Secret during deployment. Client material remains local for integration tests.

Generated private keys are gitignored and are never committed.

### Why not require Teleport for the proof of concept?

Teleport Machine & Workload Identity is an appropriate production solution for issuing and rotating short-lived workload certificates. Requiring a Teleport cluster for this challenge, however, would add a substantial external dependency to a task whose minimum reviewer dependencies are Go, make, and Docker.

The proof-of-concept certificate generator keeps `make deploy` self-contained while preserving a clear production migration path to Teleport-managed workload identity.

### Kubernetes probes and mTLS

Kubelet HTTP probes do not present the application client certificate. Requiring mTLS on the only listener would therefore make HTTP-based liveness and readiness probes unnecessarily complex.

The process exposes a separate internal health port (`8081`) used by kubelet for liveness and readiness probes. The Kubernetes Service does not expose this port; it exposes only the mTLS-protected API port (`8443`).

The internal health listener provides `/livez` for liveness checks and `/readyz` for readiness checks. These endpoints do not require mTLS because they are intended only for Kubernetes probes and are not exposed through the Service.

## Security considerations

### Kubernetes RBAC

The service uses a dedicated ServiceAccount with a namespace-scoped Role through a RoleBinding.

Required permissions:

```text
deployments:       get, list, watch
deployments/scale: get, update
```

It does not need Secret API permissions because Kubernetes mounts the configured TLS Secret into the pod.

### Least privilege

The API cannot delete Deployments or modify arbitrary Deployment fields. Replica updates are performed through the Deployment `/scale` subresource. Namespace-scoped RBAC also limits the impact of compromised credentials.

### Input handling

- Deployment names are validated before Kubernetes operations.
- JSON bodies have a small maximum size.
- Unknown JSON fields are rejected.
- Replica count is bounded to `0..100` as a proof-of-concept safety guardrail rather than a Kubernetes limit.

### Container hardening

The container runs as non-root with a read-only root filesystem, privilege escalation disabled, and Linux capabilities dropped.

## Developer workflow

From a fresh clone:

```bash
make doctor
go mod download
make test
make deploy
make integration-test
```

The full validation path is:

```bash
make verify
```

`make deploy` is responsible for building the image, provisioning KIND if needed, loading the image, generating certificates, creating the TLS Secret, installing/upgrading the Helm chart, and creating the test Deployment.

No manually created Kubernetes resources are required.

## Build and release

The Dockerfile uses a multi-stage build and a distroless non-root runtime image.

For the challenge, local images are loaded directly into KIND. In a production release pipeline I would:

1. run unit tests and static checks
2. build once using a version/tag derived from the Git commit
3. scan/sign the image
4. push the immutable image to a registry
5. package/version the Helm chart
6. deploy by immutable image digest
7. run post-deployment health/smoke tests

The Helm Deployment defaults to two replicas with rolling update settings `maxUnavailable: 0` and `maxSurge: 1`, preserving API availability during normal upgrades.

## Testing

Unit tests focus on behavior rather than coverage percentage.

Key tests include:

- successful replica read
- rejected invalid replica update
- successful liveness check independent of Kubernetes connectivity
- successful readiness check when Kubernetes is available
- failed readiness check when Kubernetes is unavailable
- verification that a replica GET reads from the informer store without issuing a clientset action

The KIND integration test covers the complete packaged service, including internal liveness/readiness probes, successful mTLS API access, and rejection of a caller that does not provide a client certificate.

## Edge cases

### Deployment disappears between cache read and update

The `/scale` request returns Kubernetes `NotFound`; the caller receives `404`. The informer will also remove the cached object through its watch.

### Concurrent replica updates

Replica updates use Kubernetes optimistic concurrency through `metadata.resourceVersion`. If a `/scale` update returns a conflict because another writer modified the resource, the server re-fetches the latest `Scale` object, reapplies the requested replica count, and retries the update using a bounded conflict retry policy.

If the retry policy is exhausted, the operation fails and the error is returned through the normal API error handling path.

### Watch disconnects

`client-go` reflectors reconnect and re-list as needed. The application does not implement its own reconnect loop.

### API server temporarily unavailable

The informer may retain previously cached Deployment state while the Kubernetes API is unavailable. However, `/readyz` fails because the readiness check performs a live Kubernetes API request and cannot verify Kubernetes connectivity. Writes also fail while the API server is unavailable. Kubernetes removes the pod from Service endpoints until connectivity is restored.

`/livez` continues to succeed because the server process itself remains healthy. This avoids restarting the container for an external dependency failure and allows the Kubernetes client and informer to recover when API connectivity returns.

### Cache lag after a write

A PUT response reflects the successful live Kubernetes update, while a GET immediately afterward may briefly see the old cached value. Integration tests explicitly wait for cache convergence rather than assuming read-after-write consistency.

### Deployment replica pointer is nil

Kubernetes defaults `spec.replicas` to `1` when it is omitted from a Deployment. As a defensive fallback, the API therefore reports `1` if a cached Deployment has a nil `spec.replicas` pointer.

### Multiple API replicas

Each server pod has its own informer cache. All watches observe the same Kubernetes source of truth, so caches converge independently. No cross-pod cache coordination is necessary.

## Alternatives considered

### Poll Kubernetes on every GET

Rejected for Level 4 because the requirement explicitly calls for cached replica reads and because request load should not translate directly into Kubernetes API load.

### Custom in-memory map and watch loop

Rejected because `client-go` informers already solve initial list, watch reconnection, local storage, and synchronization semantics.

### Require Teleport for local certificate issuance

Technically valid but rejected for the proof of concept because it significantly increases reviewer setup and obscures the core Kubernetes exercise. Teleport remains a strong production certificate lifecycle option.

### ClusterRole

Rejected because the server needs only namespace-local Deployment access.
