# Kubernetes Replica Server

A deliberately small **Level 4** implementation of the Teleport SRE take-home challenge. The service exposes an HTTP API for listing Kubernetes Deployments and reading/updating replica counts, caches Deployment reads with a `client-go` informer, and protects the API with mutual TLS.

The implementation is namespace-scoped and intended to run locally with KIND on macOS or Linux.

## Requirements

Required by the challenge:

- Go 1.26+
- make
- Docker

Used by the local Kubernetes workflow:

- KIND
- kubectl
- Helm 3
- curl

Check everything with:

```bash
make doctor
```

## Fresh clone workflow

```bash
make doctor
go mod download
make test
make deploy
make integration-test
```

Or run the complete workflow:

```bash
make verify
```

`make deploy` automatically:

1. builds the container image
2. creates the KIND cluster if needed
3. loads the image into KIND
4. generates a short-lived local CA, server certificate, and client certificate
5. creates the server TLS Kubernetes Secret
6. installs/upgrades the Helm release
7. deploys a small `replica-test` Deployment for integration testing

Each execution of `make deploy` regenerates the local mTLS certificate material and updates the Kubernetes TLS Secret. The Replica Server Deployment is restarted after installation or upgrade to ensure the running application loads the current certificates.

Certificates are generated for local testing and have a 24-hour validity period. Previously generated client certificates should not be reused after certificate regeneration.

Generated private keys live under `certs/` and are gitignored. No private key or Kubernetes Secret is committed to the repository.

## API

The externally exposed service listens on HTTPS port `8443` and requires a client certificate signed by the generated CA.

| Method | Endpoint | Description |
|---|---|---|
| GET | `/api/v1/deployments` | List Deployments with cached desired replica counts |
| GET | `/api/v1/deployments/{name}/replicas` | Retrieve the cached desired replica count |
| PUT | `/api/v1/deployments/{name}/replicas` | Update the desired replica count using Kubernetes `/scale` |

### Example requests and responses

List Deployments:

```json
[
  {"name": "replica-test", "replicas": 2}
]
```

Get Deployment replicas:

```json
{"name": "replica-test", "replicas": 2}
```

PUT request body:

```json
{"replicas": 3}
```

Successful PUT response:

```json
{"name": "replica-test", "replicas": 3}
```

Replica values must be integers between **0 and 100**, inclusive.

### HTTP status codes

| Status | Description |
|---|---|
| 200 | Successful request |
| 400 | Malformed request or invalid replica count |
| 403 | Kubernetes authorization failure |
| 404 | Deployment not found |
| 502 | Other Kubernetes API failure |
| 503 | Failed readiness check |

### Calling the API manually

Start a port-forward:

```bash
kubectl --context kind-kube-replica-server \
  -n default \
  port-forward service/kube-replica-server 18443:8443
```

In another terminal:

```bash
curl \
  --cacert certs/ca.crt \
  --cert certs/client.crt \
  --key certs/client.key \
  https://localhost:18443/api/v1/deployments
```

A request without a valid client certificate is rejected during the TLS handshake.
## Level 4 caching

A namespace-scoped `client-go` shared informer performs the initial Deployment list and then watches for changes.

`GET /api/v1/deployments` and `GET /api/v1/deployments/{name}/replicas` read from the informer store. They do **not** issue a Kubernetes API call for each HTTP request.

Replica updates intentionally use the Kubernetes Deployment `/scale` subresource. Replica updates use Kubernetes optimistic concurrency, and transient conflicts are handled internally by re-fetching the latest Scale object and retrying the update with a bounded retry policy. The update response comes from Kubernetes immediately. The informer cache then converges when it receives the watch event. Because of this, cached GETs are eventually consistent immediately after a write.

## Health checks

The pod exposes a second, internal HTTP listener on port `8081` for Kubernetes liveness and readiness probes. This listener is not exposed by the Kubernetes Service. Externally accessible API traffic remains protected by mTLS on port `8443`.

The internal health listener starts before the initial informer cache synchronization. During initialization, `/livez` succeeds while `/readyz` continues to fail until the pod is ready to receive application traffic.

`GET /livez` verifies that the server process is running and able to serve requests. It does not make a Kubernetes API request, because restarting a healthy process would not resolve a temporary Kubernetes API outage.

`GET /readyz` verifies that the Deployment informer has completed its initial cache synchronization and that the server can communicate with the Kubernetes API using its configured credentials.

If Kubernetes connectivity is temporarily lost, readiness fails and the pod is removed from Service endpoints, while liveness continues to succeed so the container is not unnecessarily restarted.

## Security

The Helm chart creates a namespace-scoped ServiceAccount, Role, and RoleBinding. Permissions are limited to:

- `get`, `list`, `watch` on Deployments;
- `get`, `update` on `deployments/scale`.

The workload does not require `cluster-admin`, Secret API access, Deployment deletion, or arbitrary Deployment updates.

The HTTP API requires TLS 1.3 and mutual TLS, with client certificates verified against the configured CA.

For this proof of concept, certificate issuance is intentionally local and automated by `make certs`. In production, certificate lifecycle management should be delegated to a workload identity or PKI system such as Teleport Machine & Workload Identity, cert-manager, or SPIFFE/SPIRE so credentials are short-lived and rotated automatically.

## Helm upgrade availability

The chart defaults to two server replicas and uses a rolling update strategy with:

```yaml
maxUnavailable: 0
maxSurge: 1
```

`maxUnavailable: 0` keeps the existing available capacity running during a rolling upgrade, while `maxSurge: 1` allows Kubernetes to start a replacement pod before removing an old pod.

Readiness probes prevent a replacement pod from receiving Service traffic until its informer cache is synchronized and it can reach the Kubernetes API.

## Tests

### Unit tests

Unit tests use a fake Kubernetes client and the Kubernetes `client-go` fake clientset to verify:

- Successful Deployment listing and replica retrieval.
- Rejection of replica counts outside the supported range.
- HTTP 403 mapping for Kubernetes authorization failures.
- HTTP 502 mapping for general Kubernetes API failures.
- Successful liveness checks, including when the Kubernetes client reports an error.
- HTTP 200 readiness responses when the client is healthy.
- HTTP 503 readiness responses when the client reports a readiness failure.
- Successful informer cache synchronization and readiness.
- Replica reads use the informer cache without issuing additional Kubernetes API calls.
- Replica updates use the Kubernetes Deployment `/scale` subresource.
- Transient Kubernetes update conflicts are retried successfully.

Run unit tests:

```bash
make test
```

### Integration tests

Integration tests execute against the application deployed into a local KIND cluster.

The test suite verifies:

- Successful liveness and readiness checks.
- API access using a valid mTLS client certificate.
- Rejection of requests without a client certificate.
- Reading cached Deployment replica counts.
- Updating desired replicas through the PUT endpoint.
- Independently verifying the updated desired replica count through the Kubernetes API.
- Eventual informer cache convergence after an update.
- Deployment listing.
- HTTP 404 for unknown Deployments.
- HTTP 400 for replica counts outside the supported range.
- HTTP 400 for malformed JSON.
- Restoration of the test Deployment to its initial desired replica count.

Run integration tests:

```bash
make integration-test
```

Run the complete verification workflow:

```bash
make verify
```

The integration suite uses a dedicated `replica-test` Deployment and restores its desired replica count to `2` when testing completes.

Error scenarios that require controlled Kubernetes failures, such as authorization errors and API connectivity failures, can be verified through focused Go tests rather than deliberately disrupting the KIND cluster during every integration run.

## Cleanup

Remove generated certificates, the local build output, and the KIND cluster:

```bash
make clean 
```

This deletes the local Kubernetes environment, including the Helm release and test Deployment.

## Design document

See [`rfd/0001-kubernetes-replica-server.md`](rfd/0001-kubernetes-replica-server.md).

