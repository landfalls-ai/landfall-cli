package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const invokePath = "/o/acme/incidents/inc-1/plugins/cloudwatch/invoke"

const (
	albArn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web-edge-alb/50dc6c495c0c9188"
	nlbArn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/tcp-nlb/aaaa"
	tgArn  = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-edge-tg/73e2d6bc24d8a067"
	tg2Arn = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/other-tg/1111"
)

// cloudwatchFake answers the CloudWatch source's operations the way the
// invoke route does: the RawResult envelope with the SDK answer under raw.
type cloudwatchFake struct {
	ops       []string
	metricReq map[string]any
	answers   map[string]any // operation → raw, or an envelope override
}

func (c *cloudwatchFake) install(f *fakeLandfall) {
	f.handle("POST "+invokePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+roomToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		op, _ := body["operation"].(string)
		c.ops = append(c.ops, op)
		params, _ := body["params"].(map[string]any)
		if op == "getMetricData" {
			c.metricReq = params
		}
		ans, ok := c.answers[op]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if env, isEnv := ans.(map[string]any); isEnv && env["partial"] != nil {
			writeJSON200(w, env)
			return
		}
		writeJSON200(w, map[string]any{"source": "cloudwatch", "operation": op, "params": params, "fetchedAt": "2026-10-08T16:00:00Z", "raw": ans})
	})
}

func lbFixture() *cloudwatchFake {
	return &cloudwatchFake{answers: map[string]any{
		"listLoadBalancers": map[string]any{"LoadBalancers": []any{
			map[string]any{"LoadBalancerArn": albArn, "LoadBalancerName": "web-edge-alb", "Type": "application", "Scheme": "internet-facing", "State": map[string]any{"Code": "active"},
				"AvailabilityZones": []any{map[string]any{"ZoneName": "us-east-1a"}, map[string]any{"ZoneName": "us-east-1c"}}},
			map[string]any{"LoadBalancerArn": nlbArn, "LoadBalancerName": "tcp-nlb", "Type": "network"},
		}},
		"listTargetGroups": map[string]any{"TargetGroups": []any{
			map[string]any{"TargetGroupArn": tgArn, "TargetGroupName": "web-edge-tg", "Port": 8080, "Protocol": "HTTP", "HealthCheckPath": "/healthz", "LoadBalancerArns": []any{albArn}},
			map[string]any{"TargetGroupArn": tg2Arn, "TargetGroupName": "other-tg", "Port": 80, "Protocol": "TCP", "LoadBalancerArns": []any{nlbArn}},
		}},
		"describeTargetHealth": map[string]any{"TargetHealthDescriptions": []any{
			map[string]any{"Target": map[string]any{"Id": "i-0c3f", "Port": 8080, "AvailabilityZone": "us-east-1c"}, "TargetHealth": map[string]any{"State": "unhealthy", "Reason": "Target.ResponseCodeMismatch", "Description": "Health checks failed with these codes: [502]"}},
			map[string]any{"Target": map[string]any{"Id": "i-0a3f", "Port": 8080, "AvailabilityZone": "us-east-1a"}, "TargetHealth": map[string]any{"State": "healthy"}},
		}},
		"getMetricData": map[string]any{"MetricDataResults": []any{
			map[string]any{"Id": "h0", "Label": "web-edge-tg", "Timestamps": []any{"2026-10-08T15:57:00Z", "2026-10-08T15:58:00Z"}, "Values": []any{2.0, 1.0}, "StatusCode": "Complete"},
			map[string]any{"Id": "e0", "Label": "web-edge-tg", "Timestamps": []any{"2026-10-08T15:58:00Z"}, "Values": []any{5.0}},
			map[string]any{"Id": "r0", "Label": "web-edge-tg", "Timestamps": []any{"2026-10-08T15:57:00Z", "2026-10-08T15:58:00Z"}, "Values": []any{100.0, 50.0}},
		}},
	}}
}

func TestLBReadsTheRoomsALBsThroughTheCloudWatchSource(t *testing.T) {
	f := newFakeLandfall(t)
	cw := lbFixture()
	cw.install(f)
	ans := roundTrip(t, RunLB(context.Background(), LBOptions{Minutes: 5}, f.deps(false)))
	if ans["ok"] != true || ans["region"] != "us-east-1" || ans["account"] != "123456789012" || ans["fiveXxBy"] != "targetGroup" {
		t.Fatalf("answer: %v", ans)
	}
	if strings.Join(cw.ops, ",") != "listLoadBalancers,listTargetGroups,describeTargetHealth,getMetricData" {
		t.Fatalf("operations = %v (the network LB's group must not be read)", cw.ops)
	}
	lbs := ans["loadBalancers"].([]any)
	if len(lbs) != 1 {
		t.Fatalf("only Application Load Balancers: %v", lbs)
	}
	lb := lbs[0].(map[string]any)
	if lb["name"] != "web-edge-alb" || lb["healthy"] != float64(1) || lb["total"] != float64(2) || lb["minuteStartMs"] != float64(time.Date(2026, 10, 8, 15, 55, 0, 0, time.UTC).UnixMilli()) {
		t.Fatalf("lb: %v", lb)
	}
	tg := lb["targetGroups"].([]any)[0].(map[string]any)
	if tg["name"] != "web-edge-tg" || tg["port"] != float64(8080) || tg["healthCheck"] != "/healthz" {
		t.Fatalf("tg: %v", tg)
	}
	targets := tg["targets"].([]any)
	if first := targets[0].(map[string]any); first["zone"] != "us-east-1a" || first["state"] != "healthy" {
		t.Fatalf("targets sorted by zone: %v", targets)
	}
	if second := targets[1].(map[string]any); second["reason"] != "Target.ResponseCodeMismatch" || second["detail"] == nil {
		t.Fatalf("unhealthy target: %v", second)
	}
	// Five minutes, 15:55 to 15:59: counts default to 0, gauges to null.
	wantJSON(t, tg["healthyHostCount"], `[null,null,2,1,null]`)
	wantJSON(t, tg["fiveXx"], `[0,0,0,5,0]`)
	wantJSON(t, tg["requests"], `[0,0,100,50,0]`)
	wantJSON(t, tg["fiveXxPct"], `[null,null,0,10,null]`)
	wantJSON(t, lb["healthyHostCount"], `[null,null,2,1,null]`)

	// The metric read: per target group, CloudWatch's own dimension values.
	queries := cw.metricReq["MetricDataQueries"].([]any)
	if len(queries) != 3 {
		t.Fatalf("queries: %v", queries)
	}
	dims := queries[1].(map[string]any)["MetricStat"].(map[string]any)["Metric"].(map[string]any)["Dimensions"].([]any)
	if dims[0].(map[string]any)["Value"] != "targetgroup/web-edge-tg/73e2d6bc24d8a067" || dims[1].(map[string]any)["Value"] != "app/web-edge-alb/50dc6c495c0c9188" {
		t.Fatalf("dimensions: %v", dims)
	}
	if cw.metricReq["StartTime"] != "2026-10-08T15:55:00Z" || cw.metricReq["EndTime"] != "2026-10-08T16:00:00Z" {
		t.Fatalf("window: %v", cw.metricReq)
	}

	// `query` is the 5xx read, re-runnable by `landfall chart`.
	q := ans["query"].(map[string]any)
	if q["source"] != "cloudwatch" || q["operation"] != "getMetricData" || q["title"] != "5xx per target group · web-edge-alb" {
		t.Fatalf("query: %v", q)
	}
	qq := q["params"].(map[string]any)["MetricDataQueries"].([]any)
	if len(qq) != 1 || qq[0].(map[string]any)["MetricStat"].(map[string]any)["Metric"].(map[string]any)["MetricName"] != "HTTPCode_Target_5XX_Count" {
		t.Fatalf("query params: %v", qq)
	}
	var cq chartQuery
	b, _ := json.Marshal(q)
	if err := json.Unmarshal(b, &cq); err != nil || cq.Source != "cloudwatch" || cq.Title == "" {
		t.Fatalf("`landfall chart --query` must read it: %v %+v", err, cq)
	}
}

func TestLBPicksOneByNameOrArn(t *testing.T) {
	for _, sel := range []string{"web-edge-alb", albArn} {
		f := newFakeLandfall(t)
		lbFixture().install(f)
		ans := RunLB(context.Background(), LBOptions{LB: sel}, f.deps(false))
		if ans["ok"] != true || len(ans["loadBalancers"].([]map[string]any)) != 1 || ans["minutes"] != lbDefaultMinutes {
			t.Fatalf("--lb %s: %v", sel, ans)
		}
	}
}

func TestLBErrors(t *testing.T) {
	ctx := context.Background()
	wantFailure(t, RunLB(ctx, LBOptions{}, noRoomDeps()), msgNoRoom)

	f := newFakeLandfall(t)
	cw := lbFixture()
	cw.install(f)
	wantFailure(t, RunLB(ctx, LBOptions{LB: "nope"}, f.deps(false)), "The room's AWS connection has no Application Load Balancer named nope.")

	cw.answers["listLoadBalancers"] = map[string]any{"LoadBalancers": []any{}}
	wantFailure(t, RunLB(ctx, LBOptions{}, f.deps(false)), "The room's AWS connection lists no Application Load Balancers in this region.")

	delete(cw.answers, "listLoadBalancers") // the route answers 404: no such source
	wantFailure(t, RunLB(ctx, LBOptions{}, f.deps(false)), "The room has no AWS connection that can read load balancers.")

	cw.answers["listLoadBalancers"] = map[string]any{"partial": true, "error": "AccessDenied: not authorized to perform elasticloadbalancing:DescribeLoadBalancers"}
	wantFailure(t, RunLB(ctx, LBOptions{}, f.deps(false)), "The room's AWS connection is not allowed to read load balancers.")

	// A group whose health cannot be read, and metrics that cannot: still an
	// answer, saying which part is missing.
	f2 := newFakeLandfall(t)
	cw2 := lbFixture()
	cw2.answers["describeTargetHealth"] = map[string]any{"partial": true, "error": "throttled"}
	cw2.answers["getMetricData"] = map[string]any{"partial": true, "error": "AccessDenied"}
	cw2.install(f2)
	ans := RunLB(ctx, LBOptions{}, f2.deps(false))
	if ans["ok"] != true || ans["metricsUnavailable"] != "The room's AWS connection is not allowed to read load balancers." {
		t.Fatalf("answer: %v", ans)
	}
	tg := ans["loadBalancers"].([]map[string]any)[0]["targetGroups"].([]map[string]any)[0]
	if tg["healthUnavailable"] == nil || len(tg["targets"].([]map[string]any)) != 0 {
		t.Fatalf("tg: %v", tg)
	}
}

func wantJSON(t *testing.T, v any, want string) {
	t.Helper()
	b, _ := json.Marshal(v)
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}
