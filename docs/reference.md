# Reference

## `pkg/cluster`

### `Outputs`

| Field | Type | Meaning |
| --- | --- | --- |
| `Provider` | `Provider` | The kind of component that made the cluster (`eks`, `talos`). Informational. |
| `Name` | `string` | The cluster's name, as the caller supplied it; a DNS-1123 label. |
| `Endpoint` | `string` | The API server address, an `https` URL. |
| `CertificateAuthorityPEM` | `string` | The PEM bundle to trust the API server against; PEM text, not base64. |
| `OIDCIssuer` | `string` | The `https` URL that signs ServiceAccount tokens. Required with `WorkloadIdentity`, empty otherwise. |
| `Capabilities` | `Capabilities` | The set of optional abilities the cluster offers. |

`Outputs.Validate()` returns nil, or one error that joins every problem.

### Capabilities

| Capability | Constant | A cluster that offers it guarantees |
| --- | --- | --- |
| `node-pools` | `NodePools` | Capacity can be declared in groups with their own shape, and scales on demand. |
| `storage` | `Storage` | A default StorageClass provisions persistent volumes dynamically. |
| `network-policy` | `NetworkPolicy` | NetworkPolicy objects are enforced, not merely accepted. |
| `workload-identity` | `WorkloadIdentity` | A pod's ServiceAccount can be exchanged for credentials outside the cluster. |
| `load-balancing` | `LoadBalancing` | A LoadBalancer Service or a Gateway gets an externally reachable address. |
| `api-access` | `APIAccess` | The API server is reachable from the places the caller named, at `Endpoint`. |

`All()` lists them in a stable order. `NewCapabilities`, `Has` and `List`
build, query and enumerate a set. A capability this version does not define
is refused by `Validate`.

## Provider components

None yet. Each provider's page lands here with its inputs, its children and
the alias each child carries.
