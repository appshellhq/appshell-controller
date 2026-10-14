# appshell-controller

A Kubernetes controller that keeps applications in an [appshell](https://github.com/appshellhq/appshell)
registry at the state declared in your cluster. GitOps for micro-frontend activation: put an
`AppshellApplication` in git, let Argo CD or Flux sync it, and the controller reconciles the
registry to it, continuously, correcting drift.

```yaml
apiVersion: registry.appshell.org/v1alpha1
kind: AppshellApplication
metadata:
  name: storefront
spec:
  name: acme/storefront        # the registry application, as scope/name
  shell:
    root: ContainerModule/Container
    title: Storefront
  packages:                    # the full set, each pinned; anything else is deactivated
    - acme/catalog@1.2.0
    - acme/cart@2.0.1
  deletionPolicy: Orphan       # the default: deleting this resource leaves the application alone
```

> **Status: in development** (`v1alpha1`). Tracked in
> [appshellhq/appshell#25](https://github.com/appshellhq/appshell/issues/25).

## Install

The chart is published to GHCR as an OCI artifact, with the image it installs pinned by digest:

```sh
helm install appshell-controller oci://ghcr.io/appshellhq/charts/appshell-controller \
  --version <version> --namespace appshell-system --create-namespace \
  --set registry.url=https://registry.example.com
```

Then trust it on each application it should apply. The chart's notes print the command with this
release's namespace and service account filled in. See the chart's
[values](charts/appshell-controller/values.yaml) for the rest: the namespaces it watches, the
reconcile interval, metrics, the network policy.

### Verify what you install

Every release's image and chart are signed with Sigstore, keylessly, by the release workflow
itself, and carry SLSA build provenance:

```sh
cosign verify ghcr.io/appshellhq/appshell-controller:<version> \
  --certificate-identity-regexp '^https://github.com/appshellhq/appshell-controller/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/appshellhq/appshell-controller:<version> --owner appshellhq
```

The same works for `ghcr.io/appshellhq/charts/appshell-controller:<version>`.

### Argo CD health

[`charts/appshell-controller/argocd/health.lua`](charts/appshell-controller/argocd/health.lua) maps
an AppshellApplication's status to Argo CD health: Progressing until the controller has applied
the current generation, then Healthy or Degraded from its `Ready` condition. Add it to `argocd-cm`
as `resource.customizations.health.registry.appshell.org_AppshellApplication`.

## How it works

The registry's `POST /v1/apply` is the one primitive: it reconciles an application against a
declared resource, activating what differs and deactivating what's missing. The CLI's
`appshell app apply`, Terraform and this controller all drive it with the same resource. The spec
here mirrors that resource, so a file and an `AppshellApplication` say the same thing in the same
words.

**No stored secret.** The controller authenticates with its own projected service-account token:
audience-bound, short-lived, and rotated by the kubelet. The registry accepts it under
*trusted deployment*: a record on the application, added by one of its owners, that names this
cluster's issuer and the controller's service account.

```sh
appshell trust add acme --application storefront \
  --kubernetes <cluster issuer> --namespace <controller namespace> --service-account <controller account>
```

The registry needs to read your cluster's public OIDC keys. EKS, GKE and AKS publish them. A
registry running in the same cluster as the controller can trust that cluster directly
(`TRUSTED_DEPLOYMENT_LOCAL_CLUSTER`).

**Where the registry is, and the audience of the token presented to it, are the controller's
configuration, never a resource's.** If a resource could name a URL, anyone able to create one could
send the controller's token wherever they liked.

**Who can apply.** The controller applies every `AppshellApplication` it watches under one
identity, so who may use that identity is decided by two things, as with who may create Argo CD
Applications:

- who may `create` `appshellapplications`, in Kubernetes RBAC;
- which namespaces the controller watches: `--watch-namespaces=a,b` limits its cache, and so
  everything it sees and acts on, to those. Left empty, it watches every namespace, and the
  manifests here grant it a ClusterRole to match.

## What it reports

`kubectl get aapp` shows each resource's application and whether it's `Ready`:

| Ready | Reason | Means | Retried |
|---|---|---|---|
| True | `Applied` | The registry application matches the spec. | Every `--reconcile-interval`, which reverts drift. |
| False | `NotTrusted` | No trusted-deployment record on the application matches the controller, or the application doesn't exist. | Every minute, until one does. |
| False | `Unauthenticated` | The registry refused the token: wrong audience, or an issuer it doesn't trust. | Every minute. |
| False | `Invalid` | The registry refused the spec: a package not published, a field it doesn't accept. | Every minute. |
| False | `RegistryUnavailable` | The registry couldn't be reached, or answered with its own error. | With backoff. |
| False | `DeactivationFailed` | A `Delete` policy couldn't deactivate, so the resource is held. | Every minute if refused; with backoff if the registry couldn't be reached. |

An Event says what moved when an apply changed anything, and once when a refusal begins.

## Deletion

`deletionPolicy` decides what deleting a resource does:

- **`Orphan`** (the default): nothing. The application keeps its packages and simply stops being
  reconciled. No finalizer is held, so a delete never waits on the registry.
- **`Delete`**: the controller deactivates every package in the application, and only then lets
  the resource go. Its settings are left as they are. If deactivation can't succeed, the resource
  is held, with a condition saying why; switching it to `Orphan` lets it go. It takes effect once
  the controller has reconciled the resource: one deleted before that was never applied, and goes.

## Configuration

| Flag | Default | |
|---|---|---|
| `--registry-url` | (required) | The registry, over https. |
| `--registry-token-path` | `/var/run/secrets/appshell/token` | The projected service-account token, minted for the registry's deploy audience. |
| `--reconcile-interval` | `10m` | How often an applied resource is applied again. At least a minute. |
| `--watch-namespaces` | every namespace | Comma-separated namespaces to reconcile. |
| `--allow-insecure-registry` | `false` | Allow a plain http registry. Local development only. |

The token comes from a projected volume; `config/manager/manager.yaml` shows it, with a placeholder
registry for an overlay to replace:

```yaml
- name: registry-token
  projected:
    sources:
      - serviceAccountToken:
          audience: https://registry.example.com/deploy   # the registry's deploy audience
          expirationSeconds: 600
          path: token
```

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from
conventional commits. It keeps a release PR open with the next version and the changelog; merging
it tags `vX.Y.Z`, and the release workflow builds the multi-arch image, signs it, attests its
provenance and SBOM, and publishes the chart with the image pinned by digest. It runs in a
`release` environment only `main` may deploy from, so nothing but a merged release can publish.

Before 1.0, a `feat:` bumps the minor version and a `fix:` the patch.

## Development

Requires Go (the version in `go.mod`), Docker, and [kind](https://kind.sigs.k8s.io/) for the e2e
tests.

```sh
make test        # unit tests, and controller tests against a real API server (envtest)
make lint        # golangci-lint, with Kubernetes logging conventions
make manifests   # regenerate the CRD and RBAC from the types and markers
make test-e2e    # deploy into a throwaway kind cluster and test it there
make chart-crds  # regenerate the chart's CRD template from the generated CRD
```

The CRD in `config/crd/bases` and the deepcopy code are generated from `api/v1alpha1`; CI fails if
they aren't committed as generated.

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

[Apache License 2.0](LICENSE).
