---
title: "Environment variables"
weight: 60
description: "The environment variables that the server, the updater and the tools read."
---

This page is generated from the configuration loader, the environment reads of the programs and the deployment templates by `make docs-gen`. Do not edit it by hand: `make docs-check` fails when it differs from the code.

## Configuration overrides

Every scalar setting of the [server configuration](configuration.md) can be overridden with an environment variable. The name is `MYRIARCH_` followed by the YAML path in upper case, joined with `_`. A camelCase key is not split: `server.publicUrl` becomes `MYRIARCH_SERVER_PUBLICURL`.

- A list is given as comma separated values, for example `MYRIARCH_AUTH_OIDC_SCOPES=openid,profile,email`.
- A duration uses Go syntax such as `30s` or `5m`. A plain integer is rejected.
- An empty value is ignored for a boolean, a number and a duration. For a string it sets the empty string.
- The overrides apply to `myriarchd serve`, `myriarchd dev`, `myriarchd migrate`, `myriarchd config validate` and `myriarchd config print`.
- An override wins over the configuration file.
- Lists of objects and maps (`opamp.enrollmentTokens`, `notifications.channels`, `auth.devLogin.users` and the `executors` maps) cannot be set through the environment.

| Variable | Key | Type |
|---|---|---|
| `MYRIARCH_ARTIFACTS_AGENTBASEURLS` | [`artifacts.agentBaseUrls`](configuration.md#artifacts) | list of strings |
| `MYRIARCH_ARTIFACTS_ALLOWEDREDIRECTHOSTS` | [`artifacts.allowedRedirectHosts`](configuration.md#artifacts) | list of strings |
| `MYRIARCH_ARTIFACTS_ALLOWINSECUREAGENTBASEURLS` | [`artifacts.allowInsecureAgentBaseUrls`](configuration.md#artifacts) | boolean |
| `MYRIARCH_ARTIFACTS_ALLOWUNVERIFIED` | [`artifacts.allowUnverified`](configuration.md#artifacts) | boolean |
| `MYRIARCH_ARTIFACTS_DIR` | [`artifacts.dir`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_DOWNLOADTOKENTTL` | [`artifacts.downloadTokenTtl`](configuration.md#artifacts) | duration |
| `MYRIARCH_ARTIFACTS_MAXCACHEBYTES` | [`artifacts.maxCacheBytes`](configuration.md#artifacts) | integer |
| `MYRIARCH_ARTIFACTS_PREFETCHPLATFORMS` | [`artifacts.prefetchPlatforms`](configuration.md#artifacts) | list of strings |
| `MYRIARCH_ARTIFACTS_PROXYURL` | [`artifacts.proxyUrl`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_SOURCEAUTH_PASSWORDFILE` | [`artifacts.sourceAuth.passwordFile`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_SOURCEAUTH_TOKENFILE` | [`artifacts.sourceAuth.tokenFile`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_SOURCEAUTH_USERNAME` | [`artifacts.sourceAuth.username`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_SOURCEBASEURL` | [`artifacts.sourceBaseUrl`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_VERIFY_CERTIFICATEIDENTITYREGEXP` | [`artifacts.verify.certificateIdentityRegexp`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_VERIFY_CERTIFICATEOIDCISSUER` | [`artifacts.verify.certificateOidcIssuer`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_VERIFY_REQUIRECHECKSUM` | [`artifacts.verify.requireChecksum`](configuration.md#artifacts) | boolean |
| `MYRIARCH_ARTIFACTS_VERIFY_REQUIRESIGNATURE` | [`artifacts.verify.requireSignature`](configuration.md#artifacts) | boolean |
| `MYRIARCH_ARTIFACTS_VERIFY_TRUSTEDROOTFILE` | [`artifacts.verify.trustedRootFile`](configuration.md#artifacts) | string |
| `MYRIARCH_ARTIFACTS_VERIFY_TUFREPOSITORYURL` | [`artifacts.verify.tufRepositoryUrl`](configuration.md#artifacts) | string |
| `MYRIARCH_AUTH_APITOKENMAXTTL` | [`auth.apiTokenMaxTtl`](configuration.md#auth) | duration |
| `MYRIARCH_AUTH_BOOTSTRAPADMIN_ENABLED` | [`auth.bootstrapAdmin.enabled`](configuration.md#auth) | boolean |
| `MYRIARCH_AUTH_BOOTSTRAPADMIN_KEEPWITHOIDC` | [`auth.bootstrapAdmin.keepWithOidc`](configuration.md#auth) | boolean |
| `MYRIARCH_AUTH_BOOTSTRAPADMIN_PASSWORDHASHFILE` | [`auth.bootstrapAdmin.passwordHashFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_BOOTSTRAPADMIN_TOKENFILE` | [`auth.bootstrapAdmin.tokenFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_BOOTSTRAPADMIN_USERNAME` | [`auth.bootstrapAdmin.username`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_COOKIESECURE` | [`auth.cookieSecure`](configuration.md#auth) | boolean |
| `MYRIARCH_AUTH_DEVLOGIN_ENABLED` | [`auth.devLogin.enabled`](configuration.md#auth) | boolean |
| `MYRIARCH_AUTH_DOWNLOADTOKENSECRETFILE` | [`auth.downloadTokenSecretFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_CLIENTCERTIFICATEFILE` | [`auth.oidc.clientCertificateFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_CLIENTID` | [`auth.oidc.clientId`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_CLIENTKEYFILE` | [`auth.oidc.clientKeyFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_CLIENTSECRET` | [`auth.oidc.clientSecret`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_CLIENTSECRETFILE` | [`auth.oidc.clientSecretFile`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_DEFAULTROLE` | [`auth.oidc.defaultRole`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_DISPLAYNAME` | [`auth.oidc.displayName`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_EMAILCLAIM` | [`auth.oidc.emailClaim`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_ENABLED` | [`auth.oidc.enabled`](configuration.md#auth) | boolean |
| `MYRIARCH_AUTH_OIDC_GROUPSCLAIM` | [`auth.oidc.groupsClaim`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_ISSUERURL` | [`auth.oidc.issuerUrl`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_NAMECLAIM` | [`auth.oidc.nameClaim`](configuration.md#auth) | string |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_ADMIN` | [`auth.oidc.roleMapping.admin`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_OPERATOR` | [`auth.oidc.roleMapping.operator`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_ADMIN` | [`auth.oidc.roleMapping.users.admin`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_OPERATOR` | [`auth.oidc.roleMapping.users.operator`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_USERS_VIEWER` | [`auth.oidc.roleMapping.users.viewer`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_ROLEMAPPING_VIEWER` | [`auth.oidc.roleMapping.viewer`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_OIDC_SCOPES` | [`auth.oidc.scopes`](configuration.md#auth) | list of strings |
| `MYRIARCH_AUTH_SESSIONMAXAGE` | [`auth.sessionMaxAge`](configuration.md#auth) | duration |
| `MYRIARCH_AUTH_SESSIONTTL` | [`auth.sessionTtl`](configuration.md#auth) | duration |
| `MYRIARCH_DATABASE_DSN` | [`database.dsn`](configuration.md#database) | string |
| `MYRIARCH_DATABASE_DSNFILE` | [`database.dsnFile`](configuration.md#database) | string |
| `MYRIARCH_DATABASE_MAXCONNS` | [`database.maxConns`](configuration.md#database) | integer |
| `MYRIARCH_DATABASE_MIGRATEONSTART` | [`database.migrateOnStart`](configuration.md#database) | boolean |
| `MYRIARCH_DATABASE_STATEMENTTIMEOUT` | [`database.statementTimeout`](configuration.md#database) | duration |
| `MYRIARCH_DEV_DATADIR` | [`dev.dataDir`](configuration.md#dev) | string |
| `MYRIARCH_DEV_POSTGRESPORT` | [`dev.postgresPort`](configuration.md#dev) | integer |
| `MYRIARCH_DEV_SEEDENROLLMENTTOKEN` | [`dev.seedEnrollmentToken`](configuration.md#dev) | string |
| `MYRIARCH_EXECUTORS_ALLOWEDWEBHOOKHOSTS` | [`executors.allowedWebhookHosts`](configuration.md#executors) | list of strings |
| `MYRIARCH_EXECUTORS_DEFAULTTIMEOUT` | [`executors.defaultTimeout`](configuration.md#executors) | duration |
| `MYRIARCH_EXECUTORS_MAXTIMEOUT` | [`executors.maxTimeout`](configuration.md#executors) | duration |
| `MYRIARCH_EXECUTORS_SECRETDIR` | [`executors.secretDir`](configuration.md#executors) | string |
| `MYRIARCH_EXECUTORS_SECRETENVPREFIX` | [`executors.secretEnvPrefix`](configuration.md#executors) | string |
| `MYRIARCH_GITOPS_AUTH_CAFILE` | [`gitops.auth.caFile`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_AUTH_KNOWNHOSTSFILE` | [`gitops.auth.knownHostsFile`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_AUTH_SSHKEYFILE` | [`gitops.auth.sshKeyFile`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_AUTH_TOKENFILE` | [`gitops.auth.tokenFile`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_AUTH_USERNAME` | [`gitops.auth.username`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_BRANCH` | [`gitops.branch`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_CLONEDIR` | [`gitops.cloneDir`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_ENABLED` | [`gitops.enabled`](configuration.md#gitops) | boolean |
| `MYRIARCH_GITOPS_MAXFILES` | [`gitops.maxFiles`](configuration.md#gitops) | integer |
| `MYRIARCH_GITOPS_MAXREPOBYTES` | [`gitops.maxRepoBytes`](configuration.md#gitops) | integer |
| `MYRIARCH_GITOPS_MAXYAMLBYTES` | [`gitops.maxYamlBytes`](configuration.md#gitops) | integer |
| `MYRIARCH_GITOPS_PATH` | [`gitops.path`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_POLLINTERVAL` | [`gitops.pollInterval`](configuration.md#gitops) | duration |
| `MYRIARCH_GITOPS_REPOURL` | [`gitops.repoUrl`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_UIREPOWEBURL` | [`gitops.uiRepoWebUrl`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_WEBHOOKSECRET` | [`gitops.webhookSecret`](configuration.md#gitops) | string |
| `MYRIARCH_GITOPS_WEBHOOKSECRETFILE` | [`gitops.webhookSecretFile`](configuration.md#gitops) | string |
| `MYRIARCH_HA_HEARTBEATINTERVAL` | [`ha.heartbeatInterval`](configuration.md#ha) | duration |
| `MYRIARCH_HA_LEADERLOCKKEY` | [`ha.leaderLockKey`](configuration.md#ha) | integer |
| `MYRIARCH_HA_REPLICAID` | [`ha.replicaId`](configuration.md#ha) | string |
| `MYRIARCH_OPAMP_ALLOWUNBOUNDEDCOMPRESSION` | [`opamp.allowUnboundedCompression`](configuration.md#opamp) | boolean |
| `MYRIARCH_OPAMP_AUTOREBINDONTOKENROTATION` | [`opamp.autoRebindOnTokenRotation`](configuration.md#opamp) | boolean |
| `MYRIARCH_OPAMP_CLIENTCERTHEADER` | [`opamp.clientCertHeader`](configuration.md#opamp) | string |
| `MYRIARCH_OPAMP_CONNECTRATELIMIT` | [`opamp.connectRateLimit`](configuration.md#opamp) | number |
| `MYRIARCH_OPAMP_CONNECTRATEPERIP` | [`opamp.connectRatePerIp`](configuration.md#opamp) | number |
| `MYRIARCH_OPAMP_ENABLECOMPRESSION` | [`opamp.enableCompression`](configuration.md#opamp) | boolean |
| `MYRIARCH_OPAMP_LASTSEENFLUSHINTERVAL` | [`opamp.lastSeenFlushInterval`](configuration.md#opamp) | duration |
| `MYRIARCH_OPAMP_MAXCONNECTIONSPERREPLICA` | [`opamp.maxConnectionsPerReplica`](configuration.md#opamp) | integer |
| `MYRIARCH_OPAMP_MAXCONNECTIONSPERTOKEN` | [`opamp.maxConnectionsPerToken`](configuration.md#opamp) | integer |
| `MYRIARCH_OPAMP_MAXEFFECTIVECONFIGBYTES` | [`opamp.maxEffectiveConfigBytes`](configuration.md#opamp) | integer |
| `MYRIARCH_OPAMP_MAXMESSAGESIZE` | [`opamp.maxMessageSize`](configuration.md#opamp) | integer |
| `MYRIARCH_OPAMP_OFFLINEAFTER` | [`opamp.offlineAfter`](configuration.md#opamp) | duration |
| `MYRIARCH_OPAMP_PATH` | [`opamp.path`](configuration.md#opamp) | string |
| `MYRIARCH_OPAMP_REQUIRECLIENTCERT` | [`opamp.requireClientCert`](configuration.md#opamp) | boolean |
| `MYRIARCH_OPAMP_REQUIREENROLLMENTTOKEN` | [`opamp.requireEnrollmentToken`](configuration.md#opamp) | boolean |
| `MYRIARCH_OPAMP_SENDTIMEOUT` | [`opamp.sendTimeout`](configuration.md#opamp) | duration |
| `MYRIARCH_OUTBOUND_ALLOWEDCIDRS` | [`outbound.allowedCidrs`](configuration.md#outbound) | list of strings |
| `MYRIARCH_OUTBOUND_CAFILE` | [`outbound.caFile`](configuration.md#outbound) | string |
| `MYRIARCH_RELEASES_CONTRIBREPO` | [`releases.contribRepo`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_COREREPO` | [`releases.coreRepo`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_DISTRIBUTIONS` | [`releases.distributions`](configuration.md#releases) | list of strings |
| `MYRIARCH_RELEASES_ENABLED` | [`releases.enabled`](configuration.md#releases) | boolean |
| `MYRIARCH_RELEASES_FIXTUREDIR` | [`releases.fixtureDir`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_GITHUBAPIURL` | [`releases.githubApiUrl`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_INCLUDEPRERELEASES` | [`releases.includePrereleases`](configuration.md#releases) | boolean |
| `MYRIARCH_RELEASES_MINVERSION` | [`releases.minVersion`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_POLLINTERVAL` | [`releases.pollInterval`](configuration.md#releases) | duration |
| `MYRIARCH_RELEASES_RAWBASEURL` | [`releases.rawBaseUrl`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_RELEASESREPO` | [`releases.releasesRepo`](configuration.md#releases) | string |
| `MYRIARCH_RELEASES_TOKENFILE` | [`releases.tokenFile`](configuration.md#releases) | string |
| `MYRIARCH_RETENTION_AGENTEVENTS` | [`retention.agentEvents`](configuration.md#retention) | duration |
| `MYRIARCH_RETENTION_AUDIT` | [`retention.audit`](configuration.md#retention) | duration |
| `MYRIARCH_RETENTION_FINISHEDROLLOUTS` | [`retention.finishedRollouts`](configuration.md#retention) | duration |
| `MYRIARCH_RETENTION_GITOPSSYNCHISTORY` | [`retention.gitopsSyncHistory`](configuration.md#retention) | duration |
| `MYRIARCH_RETENTION_OFFLINEAGENTS` | [`retention.offlineAgents`](configuration.md#retention) | duration |
| `MYRIARCH_RETENTION_UNREFERENCEDCONFIG` | [`retention.unreferencedConfig`](configuration.md#retention) | duration |
| `MYRIARCH_ROLLOUTS_AUTOSTART` | [`rollouts.autoStart`](configuration.md#rollouts) | boolean |
| `MYRIARCH_ROLLOUTS_DEFAULTTIMEZONE` | [`rollouts.defaultTimezone`](configuration.md#rollouts) | string |
| `MYRIARCH_ROLLOUTS_EXECUTORTIMEOUT` | [`rollouts.executorTimeout`](configuration.md#rollouts) | duration |
| `MYRIARCH_ROLLOUTS_TICKINTERVAL` | [`rollouts.tickInterval`](configuration.md#rollouts) | duration |
| `MYRIARCH_SERVER_LISTENADDR` | [`server.listenAddr`](configuration.md#server) | string |
| `MYRIARCH_SERVER_METRICSLISTENADDR` | [`server.metricsListenAddr`](configuration.md#server) | string |
| `MYRIARCH_SERVER_PUBLICURL` | [`server.publicUrl`](configuration.md#server) | string |
| `MYRIARCH_SERVER_SHUTDOWNTIMEOUT` | [`server.shutdownTimeout`](configuration.md#server) | duration |
| `MYRIARCH_SERVER_TLS_CERTFILE` | [`server.tls.certFile`](configuration.md#server) | string |
| `MYRIARCH_SERVER_TLS_CLIENTCAFILE` | [`server.tls.clientCaFile`](configuration.md#server) | string |
| `MYRIARCH_SERVER_TLS_KEYFILE` | [`server.tls.keyFile`](configuration.md#server) | string |
| `MYRIARCH_SERVER_TLS_TERMINATEDUPSTREAM` | [`server.tls.terminatedUpstream`](configuration.md#server) | boolean |
| `MYRIARCH_SERVER_TRUSTEDPROXIES` | [`server.trustedProxies`](configuration.md#server) | list of strings |
| `MYRIARCH_TELEMETRY_LOGFORMAT` | [`telemetry.logFormat`](configuration.md#telemetry) | string |
| `MYRIARCH_TELEMETRY_LOGLEVEL` | [`telemetry.logLevel`](configuration.md#telemetry) | string |
| `MYRIARCH_TELEMETRY_METRICS` | [`telemetry.metrics`](configuration.md#telemetry) | boolean |
| `MYRIARCH_UI_DEFAULTLANGUAGE` | [`ui.defaultLanguage`](configuration.md#ui) | string |
| `MYRIARCH_UI_LANGUAGES` | [`ui.languages`](configuration.md#ui) | list of strings |

## References inside configuration files

Two configuration files expand references to environment variables in their string values:

- `myriarchd.yaml` accepts `${VAR}` and `${VAR:-default}`. A variable that is unset or empty gives the default, or the empty string when there is none.
- The configuration of `myriarch-updater` accepts `${env:VAR}` and `${env:VAR:-default}`. A variable that is unset and has no default stops the updater with an error.

## Executor secrets

An `Executor` object in Git can name an environment variable of the server for a secret (`secretEnv`, `tokenEnv`), see the [Executor](gitops-schema.md#executor) reference. The name must start with `executors.secretEnvPrefix` (default `MYRIARCH_EXEC_`), so Git cannot read arbitrary variables. Each such variable needs a destination binding in `executors.secretEnvHosts` or an entry in `executors.secrets`; `myriarchd serve` refuses an executor secret without one.

## Variables that the programs read

These variables are not part of the configuration overrides.

| Variable | Read by | Effect |
|---|---|---|
| `MYRIARCH_ENROLLMENT_TOKEN` | `myriarch-sim run` | The enrollment token of the simulated collectors when `--token` is not given. Prefer it to the flag: arguments are visible in process listings. |
| `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`, `https_proxy`, `NO_PROXY`, `no_proxy` | `myriarchd` (every outbound connection), `myriarch-updater` and `myriarch-sim` (package downloads) | Forward proxy for outbound HTTP and HTTPS connections, as the Go standard library reads it (`NO_PROXY` lists the hosts that bypass the proxy). `myriarchd` exempts the host and port of the proxy from the address check (`outbound.allowedCidrs`). While a proxy is in use, `myriarchd` still refuses a destination that is given as an address in a denied range, and `localhost`; other host names are left to the egress policy of the proxy. `ALL_PROXY` is not used. |
| `SSL_CERT_FILE`, `SSL_CERT_DIR` | `myriarchd`, `myriarch-updater` and `myriarch-sim` (Go standard library, on Linux and other Unix systems except macOS) | The CA bundle file and the CA directories that replace the default locations of the system trust store. The binaries carry an embedded copy of the Mozilla roots that Go uses only when the system provides no roots at all. `outbound.caFile` adds CA certificates on top of whichever roots are in use. |
| `PGTEST_CACHE_DIR` | `myriarchd dev`, in builds with the `devpg` build tag (`make build`, `make dev`; the release binaries are built without it) | The directory that caches the PostgreSQL binaries of the embedded database (default: `myriarch/embedded-postgres` below the user cache directory of the operating system). The test helper shares it. |
| `PATH` | `myriarch-updater` on Unix systems, when it runs as root | The only variable of its own environment that the updater passes to the configuration check that it runs under the account of the supervisor (with `HOME` and `USER` of that account). |

## Variables in the deployment templates

The templates under `deploy/supervisor` and `deploy/updater` reference these variables as `${env:NAME}`. They are resolved by the OpAMP Supervisor or by `myriarch-updater` when they load their configuration, not by `myriarchd`.

| Variable | Templates | Meaning |
|---|---|---|
| `ENVIRONMENT` | `deploy/supervisor/k8s/supervisor.yaml` | Attribute `deployment.environment.name` that the supervisor in the Kubernetes template reports. |
| `K8S_CLUSTER_NAME` | `deploy/supervisor/k8s/supervisor.yaml` | Kubernetes attribute `k8s.cluster.name` that the supervisor in the Kubernetes template reports. |
| `K8S_NAMESPACE_NAME` | `deploy/supervisor/k8s/supervisor.yaml` | Kubernetes attribute `k8s.namespace.name` that the supervisor in the Kubernetes template reports (from the Downward API). |
| `K8S_NODE_NAME` | `deploy/supervisor/k8s/supervisor.yaml` | Kubernetes attribute `k8s.node.name` that the supervisor in the Kubernetes template reports (from the Downward API). |
| `K8S_POD_NAME` | `deploy/supervisor/k8s/supervisor.yaml` | Kubernetes attribute `k8s.pod.name` that the supervisor in the Kubernetes template reports (from the Downward API). |
| `MYRIARCH_ENROLLMENT_TOKEN` | `deploy/supervisor/k8s/supervisor.yaml`, `deploy/supervisor/linux/supervisor.yaml`, `deploy/supervisor/windows/supervisor.yaml`, `deploy/updater/container/updater.yaml` | The enrollment token. The supervisor sends it in the `Authorization: Bearer` header to the OpAMP endpoint; in container mode `myriarch-updater` sends it for the updater itself. |
| `MYRIARCH_OPAMP_ENDPOINT` | `deploy/supervisor/k8s/supervisor.yaml` | The OpAMP endpoint of the server that the supervisor in the Kubernetes template connects to. |
| `SITE_COUNTRY` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.country` that the supervisor in the Kubernetes template reports. |
| `SITE_ID` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.id` that the supervisor in the Kubernetes template reports. |
| `SITE_NAME` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.name` that the supervisor in the Kubernetes template reports. |
| `SITE_REGION` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.region` that the supervisor in the Kubernetes template reports. |
| `SITE_TIMEZONE` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.timezone` that the supervisor in the Kubernetes template reports. |
| `SITE_TYPE` | `deploy/supervisor/k8s/supervisor.yaml` | Site attribute `site.type` that the supervisor in the Kubernetes template reports. |
