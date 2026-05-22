# Keycloak Helm Chart (testbed)

DEV-ONLY Keycloak for the vibeD testbed cluster. Imports a realm on first start
(vibeD's `vibed` realm by default) and exposes the admin console + OIDC endpoints
on a NodePort.

Recovered from `deploy/helm/vibed-keycloak/` (which lived briefly on the `v0.1.3`
branch and was dropped from `main`/`v0.2.0`). The live cluster's existing
`vibed-keycloak` release is unaffected by this chart — you'd uninstall + reinstall
under the new name to migrate.

## Install

```sh
helm install keycloak testbed/keycloak/ \
  --namespace vibed-system --create-namespace
```

After ~30-60s the admin console is reachable at `http://localhost:31888/admin`
(default creds `admin` / `admin`).

## Bundled realm

`realm/vibed-realm.json` configures:

- Realm: `vibed`
- Roles: `vibed-admin`, `vibed-user`
- Public OIDC client: `vibed-mcp` (PKCE, redirect URIs for localhost loopback)
- Pre-created users (plaintext passwords in the JSON — read it to see them)

OIDC discovery URL once the realm is imported:

```
http://localhost:31888/realms/vibed/.well-known/openid-configuration
```

## Custom realm

Drop your own realm JSON next to the bundled one and point at it:

```sh
cp my-realm.json testbed/keycloak/realm/
helm install keycloak testbed/keycloak/ \
  --namespace vibed-system --create-namespace \
  --set realm.file=my-realm.json
```

Skip the import entirely:

```sh
helm install keycloak testbed/keycloak/ \
  --namespace vibed-system --create-namespace \
  --set realm.file=
```

## Migrating from `vibed-keycloak`

```sh
helm uninstall vibed-keycloak -n vibed-system
helm install keycloak testbed/keycloak/ -n vibed-system
```

The Service name changes from `vibed-keycloak` → `keycloak`, so any consumer
referencing the in-cluster URL needs updating (e.g. vibeD's `auth.oidc.discoveryURL`
once that's restored, or the issuer URL if you point it at the in-cluster name).

## Caveats

- **Ephemeral storage.** H2 file lives in the pod; restart wipes everything except
  what's reimported from the realm ConfigMap.
- **Plaintext realm passwords.** The bundled JSON commits credentials. Fine for a
  local kind cluster; never push this to a shared environment.
- **Recreate strategy.** Single replica, no rolling update. Upgrades cause downtime.
- The `hostname` value is baked into JWT `iss` claims. If it differs between the
  outside (browser) and inside (pod), token validation breaks. The default
  `http://localhost:31888` works on kind+podman because `extraPortMappings`
  loopback resolves it identically from both sides.
