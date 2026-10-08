package cli

// lb.go — `landfall lb --room <k> [--lb <name or arn>] [--minutes 60]`
// (contract §6, spec FR-07): the room's Application Load Balancers, their
// target groups, each target's health and zone, and the last hour per minute.
//
// Every read goes through the room daemon's existing `query` op, the same read
// query_signals and `landfall chart` make, with the room's own connection and
// session, using the CloudWatch source's own operations
// (the `@landfall/signals` CloudWatch plugin, cloudwatch.plugin.ts):
//
//   - listLoadBalancers   ELBv2 DescribeLoadBalancers, no params
//   - listTargetGroups    ELBv2 DescribeTargetGroups, no params (the plugin
//     passes params to the SDK verbatim and documents a lowercase
//     `loadBalancerArn` the SDK does not read, so the groups are matched to
//     their load balancer here, from each group's LoadBalancerArns)
//   - describeTargetHealth  one per target group, {TargetGroupArn}
//   - getMetricData       one call, AWS/ApplicationELB, Period 60:
//     HealthyHostCount (Average), HTTPCode_Target_5XX_Count (Sum) and
//     RequestCount (Sum), each per target group.
//
// CloudWatch publishes ALB metrics per load balancer, target group and zone,
// never per target, so 5xx is per target group: `fiveXxBy` says so.
//
// `query` is the 5xx read, ready for `landfall chart --query` to put on the
// room's canvas as a real widget (the existing chart path).

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/landfalls-ai/landfall-cli/internal/hooks"
	"github.com/spf13/cobra"
)

const (
	lbDefaultMinutes = 60
	lbMaxMinutes     = 360
	// lbMax and lbTargetGroupsMax bound one answer: one health read per group.
	lbMax             = 3
	lbTargetGroupsMax = 12
	lbTargetsMax      = 50
)

// LBOptions are the command's flags.
type LBOptions struct {
	Room    string
	LB      string
	Minutes int
}

func newLBCommand(ui *UI) *cobra.Command {
	var lb string
	var minutes int
	c := newReadCommand(ui, "lb", func(cmd *cobra.Command, ws hooks.Workspace, room string) map[string]any {
		return RunLB(cmdContext(cmd), LBOptions{Room: room, LB: lb, Minutes: minutes}, defaultReadDeps(ws))
	})
	c.Flags().StringVar(&lb, "lb", "", "one load balancer, by name or ARN")
	c.Flags().IntVar(&minutes, "minutes", lbDefaultMinutes, "how many minutes of metrics")
	return c
}

// lbRead runs one CloudWatch operation and returns its raw SDK answer.
func lbRead(ctx context.Context, d ReadDeps, roomKey, operation string, params map[string]any) (map[string]any, string) {
	res, err := d.Query(ctx, roomKey, chartQuery{Source: "cloudwatch", Operation: operation, Params: params})
	if err != nil {
		return nil, lbRefusal(err.Error())
	}
	if partial, _ := res["partial"].(bool); partial {
		return nil, lbRefusal(jStr(res, "error"))
	}
	if raw := jObj(res, "raw"); raw != nil {
		return raw, ""
	}
	if _, hasRaw := res["raw"]; hasRaw {
		return map[string]any{}, ""
	}
	return res, ""
}

// lbRefusal is the sentence for a read the room's AWS connection could not make.
func lbRefusal(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "no such room"), strings.Contains(r, "did not answer"):
		return msgNoRoom
	case strings.Contains(r, "not connected"), strings.Contains(r, "unknown source"), strings.Contains(r, "404"):
		return "The room has no AWS connection that can read load balancers."
	case strings.Contains(r, "accessdenied"), strings.Contains(r, "not authorized"), strings.Contains(r, "403"):
		return "The room's AWS connection is not allowed to read load balancers."
	}
	return "The room's AWS connection could not read load balancers. Try again shortly."
}

// arnParts reads the region and account out of an ARN.
func arnParts(arn string) (region, account string) {
	p := strings.SplitN(arn, ":", 6)
	if len(p) < 6 {
		return "", ""
	}
	return p[3], p[4]
}

// arnResource is what follows the marker in an ARN: ":loadbalancer/" gives
// "app/name/id", ":targetgroup/" gives "name/id". CloudWatch's dimension
// values are those, the group's prefixed with "targetgroup/".
func arnResource(arn, marker string) string {
	i := strings.Index(arn, marker)
	if i < 0 {
		return ""
	}
	return arn[i+len(marker):]
}

// RunLB builds the load balancer answer.
func RunLB(ctx context.Context, o LBOptions, d ReadDeps) map[string]any {
	room, why := pickRoom(d, o.Room)
	if why != "" {
		return failure(why)
	}
	minutes := o.Minutes
	if minutes <= 0 {
		minutes = lbDefaultMinutes
	}
	if minutes > lbMaxMinutes {
		minutes = lbMaxMinutes
	}

	lbsRaw, why := lbRead(ctx, d, room.RoomKey, "listLoadBalancers", map[string]any{})
	if why != "" {
		return failure(why)
	}
	var lbs []map[string]any
	want := strings.TrimSpace(o.LB)
	for _, x := range jList(lbsRaw, "LoadBalancers") {
		m, _ := x.(map[string]any)
		if t := jStr(m, "Type"); t != "" && t != "application" {
			continue
		}
		if want != "" && jStr(m, "LoadBalancerName") != want && jStr(m, "LoadBalancerArn") != want {
			continue
		}
		lbs = append(lbs, m)
	}
	if len(lbs) == 0 {
		if want != "" {
			return failure("The room's AWS connection has no Application Load Balancer named " + want + ".")
		}
		return failure("The room's AWS connection lists no Application Load Balancers in this region.")
	}
	truncated := false
	if len(lbs) > lbMax {
		lbs, truncated = lbs[:lbMax], true
	}

	tgsRaw, why := lbRead(ctx, d, room.RoomKey, "listTargetGroups", map[string]any{})
	if why != "" {
		return failure(why)
	}
	type group struct {
		tg     map[string]any
		lbArn  string
		out    map[string]any
		qIndex int
	}
	var groups []*group
	for _, l := range lbs {
		arn := jStr(l, "LoadBalancerArn")
		for _, x := range jList(tgsRaw, "TargetGroups") {
			tg, _ := x.(map[string]any)
			for _, a := range jList(tg, "LoadBalancerArns") {
				if s, _ := a.(string); s == arn {
					if len(groups) == lbTargetGroupsMax {
						truncated = true
						break
					}
					groups = append(groups, &group{tg: tg, lbArn: arn})
				}
			}
		}
	}

	// Target health, one read per group.
	for _, g := range groups {
		g.out = map[string]any{
			"name":     jStr(g.tg, "TargetGroupName"),
			"arn":      jStr(g.tg, "TargetGroupArn"),
			"protocol": jStr(g.tg, "Protocol"),
		}
		if port, ok := jNum(g.tg, "Port"); ok {
			g.out["port"] = int64(port)
		}
		if hc := jStr(g.tg, "HealthCheckPath"); hc != "" {
			g.out["healthCheck"] = hc
		}
		healthRaw, why := lbRead(ctx, d, room.RoomKey, "describeTargetHealth", map[string]any{"TargetGroupArn": jStr(g.tg, "TargetGroupArn")})
		targets := []map[string]any{}
		if why != "" {
			g.out["healthUnavailable"] = why
		}
		for _, x := range jList(healthRaw, "TargetHealthDescriptions") {
			if len(targets) == lbTargetsMax {
				break
			}
			th, _ := x.(map[string]any)
			t, h := jObj(th, "Target"), jObj(th, "TargetHealth")
			target := map[string]any{
				"id":     jStr(t, "Id"),
				"zone":   jStr(t, "AvailabilityZone"),
				"state":  jStr(h, "State"),
				"reason": jStr(h, "Reason"),
			}
			if port, ok := jNum(t, "Port"); ok {
				target["port"] = int64(port)
			}
			if desc := jStr(h, "Description"); desc != "" {
				target["detail"] = oneLineText(desc, 160)
			}
			targets = append(targets, target)
		}
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i]["zone"] != targets[j]["zone"] {
				return fmt.Sprint(targets[i]["zone"]) < fmt.Sprint(targets[j]["zone"])
			}
			return fmt.Sprint(targets[i]["id"]) < fmt.Sprint(targets[j]["id"])
		})
		g.out["targets"] = targets
	}

	// Metrics: one read for every group.
	end := d.now().UTC().Truncate(time.Minute)
	start := end.Add(-time.Duration(minutes) * time.Minute)
	var queries, fiveXxQueries []map[string]any
	for i, g := range groups {
		g.qIndex = i
		dims := []map[string]any{
			{"Name": "TargetGroup", "Value": "targetgroup/" + arnResource(jStr(g.tg, "TargetGroupArn"), ":targetgroup/")},
			{"Name": "LoadBalancer", "Value": arnResource(g.lbArn, ":loadbalancer/")},
		}
		metric := func(id, name, stat string) map[string]any {
			return map[string]any{
				"Id":    id,
				"Label": jStr(g.tg, "TargetGroupName"),
				"MetricStat": map[string]any{
					"Metric": map[string]any{"Namespace": "AWS/ApplicationELB", "MetricName": name, "Dimensions": dims},
					"Period": 60,
					"Stat":   stat,
				},
				"ReturnData": true,
			}
		}
		fx := metric(fmt.Sprintf("e%d", i), "HTTPCode_Target_5XX_Count", "Sum")
		queries = append(queries,
			metric(fmt.Sprintf("h%d", i), "HealthyHostCount", "Average"),
			fx,
			metric(fmt.Sprintf("r%d", i), "RequestCount", "Sum"),
		)
		fiveXxQueries = append(fiveXxQueries, fx)
	}
	window := map[string]any{
		"StartTime": start.Format(time.RFC3339),
		"EndTime":   end.Format(time.RFC3339),
		"ScanBy":    "TimestampAscending",
	}
	series := map[string][]*float64{}
	metricsNote := ""
	if len(queries) > 0 {
		params := map[string]any{"MetricDataQueries": queries}
		for k, v := range window {
			params[k] = v
		}
		raw, why := lbRead(ctx, d, room.RoomKey, "getMetricData", params)
		if why != "" {
			metricsNote = why
		}
		for _, x := range jList(raw, "MetricDataResults") {
			r, _ := x.(map[string]any)
			grid := make([]*float64, minutes)
			ts, vals := jList(r, "Timestamps"), jList(r, "Values")
			for i := 0; i < len(ts) && i < len(vals); i++ {
				s, _ := ts[i].(string)
				v, ok := vals[i].(float64)
				t := parseTime(s)
				if !ok || t.IsZero() {
					continue
				}
				idx := int(t.Sub(start) / time.Minute)
				if idx >= 0 && idx < minutes {
					val := v
					grid[idx] = &val
				}
			}
			series[jStr(r, "Id")] = grid
		}
	}

	// Per load balancer: its groups, health totals and per-minute series.
	out := make([]map[string]any, 0, len(lbs))
	region, account := "", ""
	for _, l := range lbs {
		arn := jStr(l, "LoadBalancerArn")
		if region == "" {
			region, account = arnParts(arn)
		}
		healthy, total := 0, 0
		lbHealthy := make([]*float64, minutes)
		var tgs []map[string]any
		for _, g := range groups {
			if g.lbArn != arn {
				continue
			}
			for _, t := range g.out["targets"].([]map[string]any) {
				total++
				if t["state"] == "healthy" {
					healthy++
				}
			}
			h := series[fmt.Sprintf("h%d", g.qIndex)]
			e := series[fmt.Sprintf("e%d", g.qIndex)]
			r := series[fmt.Sprintf("r%d", g.qIndex)]
			g.out["healthyHostCount"] = gridOut(h, false)
			g.out["fiveXx"] = gridOut(e, true)
			g.out["requests"] = gridOut(r, true)
			g.out["fiveXxPct"] = pctOut(e, r, minutes)
			for i := 0; i < minutes && h != nil; i++ {
				if h[i] != nil {
					sum := *h[i]
					if lbHealthy[i] != nil {
						sum += *lbHealthy[i]
					}
					lbHealthy[i] = &sum
				}
			}
			tgs = append(tgs, g.out)
		}
		one := map[string]any{
			"name":             jStr(l, "LoadBalancerName"),
			"arn":              arn,
			"scheme":           jStr(l, "Scheme"),
			"state":            jStr(jObj(l, "State"), "Code"),
			"healthy":          healthy,
			"total":            total,
			"targetGroups":     tgs,
			"healthyHostCount": gridOut(lbHealthy, false),
			"minuteStartMs":    start.UnixMilli(),
		}
		var zones []string
		for _, z := range jList(l, "AvailabilityZones") {
			zm, _ := z.(map[string]any)
			if zn := jStr(zm, "ZoneName"); zn != "" {
				zones = append(zones, zn)
			}
		}
		one["zones"] = zones
		out = append(out, one)
	}

	q := map[string]any{"MetricDataQueries": fiveXxQueries}
	for k, v := range window {
		q[k] = v
	}
	title := "5xx per target group"
	if len(lbs) == 1 {
		title += " · " + jStr(lbs[0], "LoadBalancerName")
	}
	ans := map[string]any{
		"ok":            true,
		"account":       account,
		"region":        region,
		"minutes":       minutes,
		"fiveXxBy":      "targetGroup",
		"loadBalancers": out,
		"query": map[string]any{
			"source": "cloudwatch", "operation": "getMetricData", "params": q, "title": title,
		},
	}
	if truncated {
		ans["truncated"] = true
	}
	if metricsNote != "" {
		ans["metricsUnavailable"] = metricsNote
	}
	return ans
}

// gridOut is a per-minute series for JSON: a missing minute is 0 for a count
// (no requests, no errors) and null for a gauge (nothing reported).
func gridOut(g []*float64, count bool) []any {
	out := make([]any, len(g))
	for i, v := range g {
		switch {
		case v != nil:
			out[i] = round2(*v)
		case count:
			out[i] = 0
		default:
			out[i] = nil
		}
	}
	return out
}

// pctOut is 5xx as a percent of requests per minute; null with no requests.
func pctOut(errs, reqs []*float64, n int) []any {
	out := make([]any, n)
	for i := 0; i < n; i++ {
		if reqs == nil || reqs[i] == nil || *reqs[i] == 0 {
			out[i] = nil
			continue
		}
		e := 0.0
		if errs != nil && errs[i] != nil {
			e = *errs[i]
		}
		out[i] = round2(100 * e / *reqs[i])
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
