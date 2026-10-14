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

> **Status: in development** (`v1alpha1`). The API and the resource's schema are in place; the
> controller doesn't yet apply to the registry. Tracked in
> [appshellhq/appshell#25](https://github.com/appshellhq/appshell/issues/25).

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
identity. Granting `create` on `appshellapplications`, and choosing which namespaces the controller
watches, decides who may use that identity, as granting `create` on Argo CD Applications does.

## Development

Requires Go (the version in `go.mod`), Docker, and [kind](https://kind.sigs.k8s.io/) for the e2e
tests.

```sh
make test        # unit tests, and controller tests against a real API server (envtest)
make lint        # golangci-lint, with Kubernetes logging conventions
make manifests   # regenerate the CRD and RBAC from the types and markers
make test-e2e    # deploy into a throwaway kind cluster and test it there
```

The CRD in `config/crd/bases` and the deepcopy code are generated from `api/v1alpha1`; CI fails if
they aren't committed as generated.

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

[Apache License 2.0](LICENSE).
