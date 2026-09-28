package narrate

import (
	"strings"
	"testing"

	"github.com/landfalls-ai/landfall-cli/internal/client"
)

// A real CloudWatch catalog entry's shape (the plugin's own descriptor), plus
// a second connection of the same source and a Loki source.
const cloudwatchEntry = `{
  "source": "cloudwatch",
  "kinds": ["metrics", "logs"],
  "connectionId": "landfall-aws",
  "label": "Production AWS",
  "accountId": "123456789012",
  "contextHint": "web-edge serves checkout through CloudFront E123ABC.",
  "operations": [
    {
      "operation": "getMetricStatistics",
      "kind": "metrics",
      "canonical": "range",
      "window": {"start": "StartTime", "end": "EndTime", "step": "Period", "format": "iso", "stepFormat": "seconds", "minStepSeconds": 60},
      "description": "CloudWatch GetMetricStatistics: a metric statistic series.",
      "params": {"Namespace": "e.g. AWS/CloudFront", "MetricName": "e.g. 5xxErrorRate", "StartTime": "RFC3339 or epoch", "EndTime": "RFC3339 or epoch", "Period": "seconds (e.g. 60)"},
      "iamActions": ["cloudwatch:GetMetricStatistics"],
      "permissionTier": "core"
    },
    {"operation": "filterLogEvents", "kind": "logs", "description": "CloudWatch Logs FilterLogEvents.", "params": {"logGroupName": "/aws/lambda/…"}}
  ]
}`

func TestRenderSignalCatalogKeepsWhatQuerySignalsNeeds(t *testing.T) {
	got := RenderSignalCatalog([]client.SignalCatalogEntry{
		client.SignalCatalogEntry(cloudwatchEntry),
		client.SignalCatalogEntry(`{"source":"cloudwatch","kinds":["metrics"],"connectionId":"staging-aws","operations":[{"operation":"getMetricStatistics","kind":"metrics","description":"x","params":{}}]}`),
		client.SignalCatalogEntry(`{"source":"loki","kinds":["logs"],"operations":[{"operation":"logs.range","kind":"logs","canonical":"range","description":"LogQL range query.","params":{"query":"LogQL, e.g. {app=\"web\"}"}}]}`),
	})

	for _, want := range []string{
		"3 telemetry sources connected. Read one with query_signals {source, operation, params}",
		"also pass the connection (and account) shown for the one you mean",
		`cloudwatch · metrics, logs · connection "landfall-aws" (Production AWS) · account "123456789012"`,
		"  getMetricStatistics (metrics, range): CloudWatch GetMetricStatistics: a metric statistic series.",
		"    params: Namespace (e.g. AWS/CloudFront), MetricName (e.g. 5xxErrorRate), StartTime (RFC3339 or epoch), EndTime (RFC3339 or epoch), Period (seconds (e.g. 60))",
		"    window: StartTime to EndTime as iso, step Period",
		"  filterLogEvents (logs): CloudWatch Logs FilterLogEvents.",
		"  about this connection: web-edge serves checkout through CloudFront E123ABC.",
		`cloudwatch · metrics · connection "staging-aws"`,
		"loki · logs",
		`  logs.range (logs, range): LogQL range query.`,
		`    params: query (LogQL, e.g. {app="web"})`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// What no agent acts on stays out, and nothing is a JSON dump.
	for _, gone := range []string{"iamActions", "cloudwatch:GetMetricStatistics", "permissionTier", `"source":`, "{\n"} {
		if strings.Contains(got, gone) {
			t.Errorf("still carries %q:\n%s", gone, got)
		}
	}
}

func TestRenderSignalCatalogWithOneConnectionPerSourceSaysNothingAboutConnections(t *testing.T) {
	got := RenderSignalCatalog([]client.SignalCatalogEntry{client.SignalCatalogEntry(cloudwatchEntry)})
	if !strings.HasPrefix(got, "1 telemetry source connected. Read one with query_signals {source, operation, params}.\n") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestRenderSignalCatalogNeverDropsAnEntryItCannotRead(t *testing.T) {
	got := RenderSignalCatalog([]client.SignalCatalogEntry{
		client.SignalCatalogEntry(`{"kinds":["metrics"],"operations":[]}`),
		client.SignalCatalogEntry(`{"source":"prometheus","kinds":["metrics"]}`),
	})
	if !strings.Contains(got, `(an entry this CLI could not read, as sent) {"kinds":["metrics"],"operations":[]}`) {
		t.Errorf("an unreadable entry was dropped:\n%s", got)
	}
	if !strings.Contains(got, "prometheus · metrics\n  (advertises no operations)") {
		t.Errorf("a source with no operations must say so:\n%s", got)
	}
}

func TestRenderSignalCatalogSaysWhenNothingIsConnected(t *testing.T) {
	for _, empty := range [][]client.SignalCatalogEntry{nil, {}} {
		if got := RenderSignalCatalog(empty); got != "No telemetry sources are connected for this incident's organization, so there is nothing to query." {
			t.Errorf("got %q", got)
		}
	}
}

func TestParamHintsKeepTheSourcesOrderAndTolerateOddValues(t *testing.T) {
	var o orderedHints
	if err := o.UnmarshalJSON([]byte(`{"b":"second","a":"first","n":3,"x":null}`)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var names []string
	for _, h := range o {
		names = append(names, h.name+"="+h.text)
	}
	if got := strings.Join(names, ","); got != "b=second,a=first,n=3,x=null" {
		t.Fatalf("got %q", got)
	}
	var none orderedHints
	if err := none.UnmarshalJSON([]byte(`null`)); err != nil || len(none) != 0 {
		t.Fatalf("null params = %v, %v", none, err)
	}
}
