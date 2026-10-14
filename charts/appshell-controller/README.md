# appshell-controller chart

Installs [appshell-controller](https://github.com/appshellhq/appshell-controller): the
AppshellApplication CRD, the controller with its own service account and least-privilege RBAC, a
secure metrics endpoint, and a network policy admitting only metrics scrapes.

```sh
helm install appshell-controller oci://ghcr.io/appshellhq/charts/appshell-controller \
  --version <version> --namespace appshell-system --create-namespace \
  --set registry.url=https://registry.example.com
```

`registry.url` is required, and must be https unless `registry.allowInsecure` is set for local
development. Every value is documented in [values.yaml](values.yaml) and checked by
[values.schema.json](values.schema.json).

The CRD is kept on uninstall (`crds.keep`), so removing the controller never deletes every
AppshellApplication with it. Upgrades update it, since it's a template rather than a `crds/` file.

[`argocd/health.lua`](argocd/health.lua) is the Argo CD health check for AppshellApplications.
