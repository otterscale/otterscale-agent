## OtterScale Agent

OtterScale Agent is a hub-and-spoke service for managing Kubernetes resources across clusters through a single API.
The **server (hub)** exposes a [ConnectRPC](https://connectrpc.com/) API and proxies requests to **agents (spokes)** running in target clusters over **Chisel reverse tunnels**.

### High-level architecture

- **Server (hub)**:
  - Exposes `otterscale.resource.v1.ResourceService` over HTTP (ConnectRPC)
  - Authenticates end-user requests via OIDC (Keycloak), then forwards the verified subject to the agent
  - Maintains an embedded Chisel reverse-tunnel server to reach per-cluster agents via loopback ports
- **Agent (spoke)**:
  - Runs in-cluster and executes Kubernetes operations using `client-go`
  - Exposes the same ConnectRPC service locally; the server reaches it through the reverse tunnel

### Requirements

- **Go**: `1.25.x` (see `go.mod`)
- **Kubernetes access**:
  - Server: optional (only needed if you enable leader election / HA in Kubernetes)
  - Agent: required (runs in-cluster or with a usable kubeconfig)
- **Tooling (dev only)**:
  - `golangci-lint` for `make lint`
  - `protoc` plus plugins for `make proto` / `make openapi` (see `Makefile`)

## Quickstart

### Build

```bash
make build
```

This produces `./bin/otterscale`.

### Run the server (hub)

```bash
./bin/otterscale server --address=:8299 --config=otterscale.yaml
```

Notes:
- Use a **fixed port** (avoid `:0`) if you plan to run multiple server replicas with leader forwarding enabled.
- On startup, the server logs the Chisel tunnel **fingerprint** and tunnel bind address.

### Leader election (optional / Kubernetes)

When running multiple server replicas in Kubernetes, the server can use a Kubernetes **Lease** for leader election and will only start the embedded tunnel server on the leader. Non-leaders can forward requests to the leader (requires a fixed `--address` port).

### Run an agent (spoke)

```bash
./bin/otterscale agent --cluster=dev --address=:8299 --config=otterscale.yaml
```

Notes:
- `--cluster` is required and selects `clusters.<name>.*` from the config.
- The agent maintains a long-lived connection to the server’s embedded Chisel tunnel server.

### Container mode

If `OTTERSCALE_CONTAINER=true` is set, both `server` and `agent` default to:
- `--address=:8299`
- `--config=/etc/app/otterscale.yaml`

Build and run:

```bash
docker build -t otterscale-agent .
docker run --rm \
  -p 8299:8299 \
  -v "$(pwd)/otterscale.yaml:/etc/app/otterscale.yaml:ro" \
  -e OTTERSCALE_CONTAINER=true \
  otterscale-agent server
```

Run an agent container (example):

```bash
docker run --rm \
  -v "$(pwd)/otterscale.yaml:/etc/app/otterscale.yaml:ro" \
  -e OTTERSCALE_CONTAINER=true \
  otterscale-agent agent --cluster=dev
```

## Configuration

There is no sample config checked into this repo. Below is a minimal `otterscale.yaml` you can start from.

```yaml
keycloak:
  # OIDC issuer URL for your Keycloak realm, for example:
  # https://keycloak.example.com/realms/<realm>
  realm_url: "https://keycloak.example.com/realms/otterscale"
  # OIDC client ID used to verify ID tokens.
  client_id: "otterscale"

tunnel:
  server:
    # Bind address for the embedded Chisel reverse-tunnel server.
    host: "0.0.0.0"
    port: "8300"
    # Provide ONE of the following:
    # key_file: "/path/to/chisel-ecdsa-privatekey.pem"
    key_seed: "change-me"

clusters:
  dev:
    agent:
      # Basic auth used by the agent when connecting to the server tunnel.
      auth:
        user: "dev-agent"
        pass: "dev-agent-password"
      # Expected server tunnel fingerprint (agent-side). This is printed by the server on startup.
      fingerprint: "SERVER_TUNNEL_FINGERPRINT"
      # Reverse tunnel port opened on the SERVER loopback interface (127.0.0.1:<tunnel_port>).
      tunnel_port: 51001
      # Agent local API port that will be exposed through the tunnel.
      api_port: 8299
```

### Required settings (minimal)

- **`keycloak.realm_url` / `keycloak.client_id`**: used by the server to verify incoming OIDC ID tokens.
- **`tunnel.server.*`**:
  - `host`/`port`: where the embedded Chisel reverse-tunnel server binds (and where agents connect).
  - `key_seed` **or** `key_file` is required; without one, the server will fail to start the tunnel server.
- **`clusters.<name>.agent.*`** (per cluster):
  - `auth.user` / `auth.pass`: basic auth credentials the agent uses to connect to the tunnel.
  - `tunnel_port`: the **server-local loopback** port reserved for that cluster (must be unique per cluster).
  - `fingerprint`: the expected Chisel server fingerprint (agent-side trust).
  - `api_port`: agent listen port (used to form the reverse tunnel target; also used if you run the agent with `--address=:0`).

### Getting the tunnel fingerprint

Start the server once and copy the `fingerprint` value from its startup logs (it is printed when the tunnel server starts), then paste it into `clusters.<name>.agent.fingerprint`.

### How tunnels are wired

For each cluster, the agent requests a Chisel reverse remote like:

- `R:127.0.0.1:<tunnel_port>:127.0.0.1:<agent_api_port>`

Then the server reaches the agent API at:

- `http://127.0.0.1:<tunnel_port>`

## API

### Protocol and docs

- **Protocol**: ConnectRPC over HTTP/1.1 and h2c (unencrypted HTTP/2)
- **OpenAPI**: see [`api/openapi.yaml`](api/openapi.yaml)
- **Protobuf**: see [`api/resource/v1/resource.proto`](api/resource/v1/resource.proto)

### HTTP endpoints

Server endpoints:
- **ConnectRPC service** (all `POST`):
  - `/otterscale.resource.v1.ResourceService/Discovery`
  - `/otterscale.resource.v1.ResourceService/Schema`
  - `/otterscale.resource.v1.ResourceService/List`
  - `/otterscale.resource.v1.ResourceService/Get`
  - `/otterscale.resource.v1.ResourceService/Create`
  - `/otterscale.resource.v1.ResourceService/Apply`
  - `/otterscale.resource.v1.ResourceService/Delete`
  - `/otterscale.resource.v1.ResourceService/Watch` (server-streaming)
- **Metrics**: `GET /metrics`
- **gRPC health / reflection**: enabled (use your Connect/gRPC tooling; exact paths depend on the client)

Agent endpoints:
- **Health**: `GET /ping` (returns `204 No Content`)
- **ConnectRPC service**: same service paths as above (intended to be reached via the server tunnel, not directly)

### Resource service

Service: `otterscale.resource.v1.ResourceService`

Main operations:
- `Discovery`: list available Kubernetes API resources in a cluster
- `Schema`: fetch JSON schema for a Kind (native resources or CRDs)
- `List`, `Get`: read resources
- `Create`: create from manifest
- `Apply`: server-side apply (SSA) for partial updates
- `Delete`: delete resources
- `Watch`: stream watch events

## Observability

- **Metrics** (server): `GET /metrics`

## Security notes

- **Server**: verifies `Authorization: Bearer <token>` using OIDC (Keycloak). The verified subject is stored in request context.
- **Server -> Agent**: the server propagates the verified subject via a trusted header `X-Otterscale-Subject`.
- **Agent**: trusts `X-Otterscale-Subject` and rejects requests without it.

Important:
- Do **not** expose the agent’s HTTP API directly to end users. It is intended to be reached only via the server-controlled reverse tunnel.

## Development

Common targets:

```bash
make build
make test
make lint
make proto
make openapi
```

## License

Apache-2.0. See [`LICENSE`](LICENSE).

