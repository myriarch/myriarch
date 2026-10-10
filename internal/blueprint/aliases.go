// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package blueprint

// Code generated from testdata/component-aliases-0.162.0.json
// (docs/design/opamp-server.md, Blueprint rendering > Validation); the unit
// test TestAliasTableMatchesFixture keeps both in sync.

// builtinAliases maps kind -> deprecated type name -> current type name for
// otelcol-contrib 0.162.0. The collector still accepts the old names
// (deprecated, logged as warning).
var builtinAliases = map[string]map[string]string{
	"exporters": {
		"azureblob":             "azure_blob",
		"azuremonitor":          "azure_monitor",
		"bmchelix":              "bmc_helix",
		"googlecloudstorage":    "google_cloud_storage",
		"honeycombmarker":       "honeycomb_marker",
		"loadbalancing":         "load_balancing",
		"otlp":                  "otlp_grpc",
		"otlphttp":              "otlp_http",
		"prometheusremotewrite": "prometheus_remote_write",
	},
	"extensions": {
		"azureauth":     "azure_auth",
		"cgroupruntime": "cgroup_runtime",
	},
	"processors": {
		"cumulativetodelta": "cumulative_to_delta",
		"deltatocumulative": "delta_to_cumulative",
		"deltatorate":       "delta_to_rate",
		"k8sattributes":     "k8s_attributes",
		"logdedup":          "log_dedup",
		"metricstarttime":   "metric_start_time",
		"metricstransform":  "metrics_transform",
		"resourcedetection": "resource_detection",
		"spanpruning":       "span_pruning",
	},
	"receivers": {
		"apachespark":           "apache_spark",
		"awscloudwatch":         "aws_cloudwatch",
		"awss3":                 "aws_s3",
		"azureblob":             "azure_blob",
		"azureeventhub":         "azure_event_hub",
		"azuremonitor":          "azure_monitor",
		"ciscoos":               "cisco_os",
		"cloudfoundry":          "cloud_foundry",
		"envoyals":              "envoy_als",
		"filelog":               "file_log",
		"filestats":             "file_stats",
		"flinkmetrics":          "flink_metrics",
		"fluentforward":         "fluent_forward",
		"googlecloudspanner":    "google_cloud_spanner",
		"hostmetrics":           "host_metrics",
		"httpcheck":             "http_check",
		"k8sobjects":            "k8s_objects",
		"kafkametrics":          "kafka_metrics",
		"kubeletstats":          "kubelet_stats",
		"macosunifiedlogging":   "macos_unified_logging",
		"mongodbatlas":          "mongodb_atlas",
		"namedpipe":             "named_pipe",
		"otlpjsonfile":          "otlp_json_file",
		"prometheusremotewrite": "prometheus_remote_write",
		"splunkenterprise":      "splunk_enterprise",
		"sqlquery":              "sql_query",
		"sshcheck":              "ssh_check",
		"tcpcheck":              "tcp_check",
		"tcplog":                "tcp_log",
		"tlscheck":              "tls_check",
		"udplog":                "udp_log",
		"webhookevent":          "webhook_event",
		"windowseventlog":       "windows_event_log",
		"windowsperfcounters":   "windows_perf_counters",
		"windowsservice":        "windows_service",
		"yanggrpc":              "yang_grpc",
	},
}
