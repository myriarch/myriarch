// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestValuesArePinned(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"Name", Name, "myriarch"},
		{"DisplayName", DisplayName, "Myriarch"},
		{"Authors", Authors, "The Myriarch Authors"},
		{"Domain", Domain, "myriarch.dev"},
		{"Website", Website, "https://myriarch.dev"},
		{"ModulePath", ModulePath, "github.com/myriarch/myriarch"},
		{"Repository", Repository, "https://github.com/myriarch/myriarch"},
		{"ServerBinary", ServerBinary, "myriarchd"},
		{"CLIBinary", CLIBinary, "myrctl"},
		{"CLIAlias", CLIAlias, "myr"},
		{"UpdaterBinary", UpdaterBinary, "myriarch-updater"},
		{"SimBinary", SimBinary, "myriarch-sim"},
		{"MCPBinary", MCPBinary, "myriarch-mcp"},
		{"EnvPrefix", EnvPrefix, "MYRIARCH_"},
		{"ExecSecretEnvPrefix", ExecSecretEnvPrefix, "MYRIARCH_EXEC_"},
		{"EnrollmentTokenEnv", EnrollmentTokenEnv, "MYRIARCH_ENROLLMENT_TOKEN"},
		{"OpAMPEndpointEnv", OpAMPEndpointEnv, "MYRIARCH_OPAMP_ENDPOINT"},
		{"AttributePrefix", AttributePrefix, "dev.myriarch."},
		{"KubernetesLabelPrefix", KubernetesLabelPrefix, "myriarch.dev/"},
		{"APIGroup", APIGroup, "myriarch.dev"},
		{"APIVersion", APIVersion, "myriarch.dev/v1alpha1"},
		{"MetricsNamespace", MetricsNamespace, "myriarch"},
		{"SimMetricsNamespace", SimMetricsNamespace, "myriarch_sim"},
		{"RemoteConfigKey", RemoteConfigKey, "myriarch.yaml"},
		{"UpdaterServiceName", UpdaterServiceName, "myriarch-updater"},
		{"UpdaterDisplayName", UpdaterDisplayName, "Myriarch Updater"},
		{"AttrCollectorInstanceUID", AttrCollectorInstanceUID, "dev.myriarch.collector.instance_uid"},
		{"AttrUpdaterInstallMethod", AttrUpdaterInstallMethod, "dev.myriarch.updater.install_method"},
		{"AttrUpdaterServerEndpoint", AttrUpdaterServerEndpoint, "dev.myriarch.updater.server_endpoint"},
		{"AttrUpdaterImageCollectorVersion", AttrUpdaterImageCollectorVersion, "dev.myriarch.updater.image_collector_version"},
		{"AttrUpdaterSupervisorService", AttrUpdaterSupervisorService, "dev.myriarch.updater.supervisor_service"},
		{"UpdateHealthComponent", UpdateHealthComponent, "dev.myriarch.update"},
		{"SessionCookie", SessionCookie, "myriarch_session"},
		{"SessionCookieHost", SessionCookieHost, "__Host-myriarch_session"},
		{"CSRFCookie", CSRFCookie, "myriarch_csrf"},
		{"OIDCStateCookie", OIDCStateCookie, "myriarch_oidc"},
		{"WebhookHeaderPrefix", WebhookHeaderPrefix, "X-Myriarch-"},
		{"DeliveryHeader", DeliveryHeader, "X-Myriarch-Delivery"},
		{"TimestampHeader", TimestampHeader, "X-Myriarch-Timestamp"},
		{"SignatureHeader", SignatureHeader, "X-Myriarch-Signature"},
		{"AWXExtraVarsKey", AWXExtraVarsKey, "myriarch"},
		{"AuthRealm", AuthRealm, "myriarch"},
		{"ArtifactsAuthRealm", ArtifactsAuthRealm, "myriarch artifacts"},
		{"APITokenPrefix", APITokenPrefix, "myra_"},
		{"EnrollmentTokenPrefix", EnrollmentTokenPrefix, "myre_"},
		{"DownloadTokenPrefix", DownloadTokenPrefix, "myrd_"},
		{"EnrollmentTokenPlaceholder", EnrollmentTokenPlaceholder, "@@MYRIARCH_ENROLLMENT_TOKEN@@"},
		{"InstallSetupDir", InstallSetupDir, "myriarch-setup"},
		{"NotifyChannel", NotifyChannel, "myriarch_events"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func reverseLabels(domain string) string {
	parts := strings.Split(domain, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".")
}

func TestDerivations(t *testing.T) {
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("derivation violated: %s", name)
		}
	}
	check("Website == https:// + Domain", Website == "https://"+Domain)
	check("Repository == https:// + ModulePath", Repository == "https://"+ModulePath)
	check("APIGroup == Domain", APIGroup == Domain)
	check("APIGroup contains a dot", strings.Contains(APIGroup, "."))
	check("APIVersion == APIGroup + /v1alpha1", APIVersion == APIGroup+"/v1alpha1")
	check("AttributePrefix is the reversed Domain", AttributePrefix == reverseLabels(Domain)+".")
	check("KubernetesLabelPrefix == Domain + /", KubernetesLabelPrefix == Domain+"/")
	check("ExecSecretEnvPrefix starts with EnvPrefix", strings.HasPrefix(ExecSecretEnvPrefix, EnvPrefix))
	check("ExecSecretEnvPrefix ends with _", strings.HasSuffix(ExecSecretEnvPrefix, "_"))
	check("SimMetricsNamespace == MetricsNamespace + _sim", SimMetricsNamespace == MetricsNamespace+"_sim")
	check("EnrollmentTokenPlaceholder wraps EnrollmentTokenEnv in @@", EnrollmentTokenPlaceholder == "@@"+EnrollmentTokenEnv+"@@")
	check("InstallSetupDir starts with Name", strings.HasPrefix(InstallSetupDir, Name))

	for name, h := range map[string]string{
		"DeliveryHeader": DeliveryHeader, "TimestampHeader": TimestampHeader, "SignatureHeader": SignatureHeader,
	} {
		if !strings.HasPrefix(h, WebhookHeaderPrefix) {
			t.Errorf("%s %q does not start with WebhookHeaderPrefix", name, h)
		}
		if http.CanonicalHeaderKey(h) != h {
			t.Errorf("%s %q is not in canonical form (%q)", name, h, http.CanonicalHeaderKey(h))
		}
	}
	for name, a := range map[string]string{
		"AttrCollectorInstanceUID":         AttrCollectorInstanceUID,
		"AttrUpdaterInstallMethod":         AttrUpdaterInstallMethod,
		"AttrUpdaterServerEndpoint":        AttrUpdaterServerEndpoint,
		"AttrUpdaterImageCollectorVersion": AttrUpdaterImageCollectorVersion,
		"AttrUpdaterSupervisorService":     AttrUpdaterSupervisorService,
		"UpdateHealthComponent":            UpdateHealthComponent,
	} {
		if !strings.HasPrefix(a, AttributePrefix) {
			t.Errorf("%s %q does not start with AttributePrefix", name, a)
		}
	}

	metricName := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	for _, ns := range []string{MetricsNamespace, SimMetricsNamespace} {
		if !metricName.MatchString(ns) {
			t.Errorf("metrics namespace %q is not a valid Prometheus name", ns)
		}
	}
}

func TestTokenPrefixes(t *testing.T) {
	prefixes := []string{APITokenPrefix, EnrollmentTokenPrefix, DownloadTokenPrefix}
	shape := regexp.MustCompile(`^myr[a-z]_$`)
	for i, p := range prefixes {
		if !shape.MatchString(p) {
			t.Errorf("token prefix %q is not 'myr' + one letter + '_'", p)
		}
		for j, q := range prefixes {
			if i != j && strings.HasPrefix(p, q) {
				t.Errorf("token prefix %q starts with %q", p, q)
			}
		}
	}
}
