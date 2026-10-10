// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"go.yaml.in/yaml/v3"

	"github.com/myriarch/myriarch/internal/brand"
)

// readSupervisorInstanceID reads instance_id from the supervisor's
// persistent_state.yaml. It returns "" without error if the file does not
// exist yet (created at the first supervisor start).
func readSupervisorInstanceID(path string) (string, error) {
	// The file is owned by the supervisor's (unprivileged) service account:
	// bounded regular-file read, and no file content in error messages.
	data, err := readRegularFile(path, maxSupervisorFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var st struct {
		InstanceID string `yaml:"instance_id"`
	}
	if err := yaml.Unmarshal(data, &st); err != nil {
		return "", fmt.Errorf("parse %s: invalid YAML", path)
	}
	if st.InstanceID == "" {
		return "", nil
	}
	id, err := uuid.Parse(st.InstanceID)
	if err != nil {
		return "", fmt.Errorf("%s: instance_id is not a UUID", path)
	}
	return id.String(), nil
}

func strAttr(k, v string) *protobufs.KeyValue {
	return &protobufs.KeyValue{Key: k, Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}}}
}

// Install methods reported as dev.myriarch.updater.install_method.
const (
	InstallMethodBinary    = "binary"
	InstallMethodContainer = "container"
)

// Updater attributes read by myriarchd (README 7.3).
const (
	AttrServerEndpoint        = brand.AttrUpdaterServerEndpoint
	AttrInstallMethod         = brand.AttrUpdaterInstallMethod
	AttrImageCollectorVersion = brand.AttrUpdaterImageCollectorVersion
)

// installInfo describes how the updater is deployed.
type installInfo struct {
	method string
	// imageCollectorVersion is the collector version baked into the image
	// (container mode only, without "v").
	imageCollectorVersion string
}

// normalizeServerEndpoint reduces the configured OpAMP endpoint to
// scheme://host:port: no path, query or credentials, port always explicit
// (80 for ws/http, 443 for wss/https). Unparsable input yields "".
func normalizeServerEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "ws", "http":
			port = "80"
		case "wss", "https":
			port = "443"
		default:
			return ""
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// buildDescription creates the AgentDescription of a binary (service) updater.
// collectorUID may be empty (supervisor state not created yet); the
// attribute is then omitted and added later.
func buildDescription(version string, self uuid.UUID, cfg Config, collectorUID string) *protobufs.AgentDescription {
	return buildDescriptionFor(installInfo{method: InstallMethodBinary}, version, self, cfg, collectorUID)
}

func buildDescriptionFor(info installInfo, version string, self uuid.UUID, cfg Config, collectorUID string) *protobufs.AgentDescription {
	if info.method == "" {
		info.method = InstallMethodBinary
	}
	host, _ := os.Hostname()
	d := &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			strAttr("service.name", AgentServiceName),
			strAttr("service.version", version),
			strAttr("service.instance.id", self.String()),
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			strAttr("os.type", runtime.GOOS),
			strAttr("host.arch", runtime.GOARCH),
			strAttr("host.name", host),
			strAttr(brand.AttrUpdaterSupervisorService, cfg.Supervisor.ServiceName),
			strAttr(AttrInstallMethod, info.method),
		},
	}
	if ep := normalizeServerEndpoint(cfg.Server.Endpoint); ep != "" {
		d.NonIdentifyingAttributes = append(d.NonIdentifyingAttributes, strAttr(AttrServerEndpoint, ep))
	}
	if info.imageCollectorVersion != "" {
		d.NonIdentifyingAttributes = append(d.NonIdentifyingAttributes, strAttr(AttrImageCollectorVersion, info.imageCollectorVersion))
	}
	if collectorUID != "" {
		d.NonIdentifyingAttributes = append(d.NonIdentifyingAttributes, strAttr(AttrCollectorInstanceUID, collectorUID))
	}
	return d
}
