package narrate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var signalsBase = time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return m
}

// roundTrip passes a Go-built payload through JSON so the renderer sees what
// the client actually decodes (numbers as float64, and so on).
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, string(b))
}

// cloudwatchLogRead is a FilterLogEvents answer of n events, one a second,
// shipped in reverse order so the renderer's sort is exercised.
func cloudwatchLogRead(t *testing.T, n int, extra map[string]any) map[string]any {
	t.Helper()
	events := make([]any, 0, n)
	for i := n - 1; i >= 0; i-- {
		events = append(events, map[string]any{
			"eventId":       fmt.Sprintf("3%055d", i),
			"ingestionTime": signalsBase.Add(time.Duration(i)*time.Second).UnixMilli() + 400,
			"logStreamName": "2026/09/28/[$LATEST]0123456789abcdef0123456789abcdef",
			"message": fmt.Sprintf("START RequestId: 5f1c2a9e-%04d Version: $LATEST\n"+
				`{"level":"error","msg":"upstream 503 from origin","attempt":%d,"route":"/checkout"}`, i, i),
			"timestamp": signalsBase.Add(time.Duration(i) * time.Second).UnixMilli(),
		})
	}
	raw := map[string]any{"events": events, "searchedLogStreams": []any{}}
	for k, v := range extra {
		raw[k] = v
	}
	return roundTrip(t, map[string]any{
		"source": "cloudwatch", "operation": "filterLogEvents",
		"params":    map[string]any{"logGroupName": "/aws/lambda/checkout"},
		"fetchedAt": "2026-09-28T14:30:00.000Z",
		"raw":       raw,
	})
}

func TestALogReadIsACountARangeAndBoundedLines(t *testing.T) {
	result := cloudwatchLogRead(t, 150, nil)
	got := RenderSignalRead("cloudwatch", "filterLogEvents", result, SignalReadOptions{})

	for _, want := range []string{
		"cloudwatch filterLogEvents: 150 log events, 2026-09-28 14:00:00Z to 14:02:29Z.\n",
		"\n14:00:00Z START RequestId: 5f1c2a9e-0000 Version: $LATEST {\"level\":\"error\"",
		"\n14:00:19Z START RequestId",
		"\n… 110 events not shown …\n",
		"\n14:02:10Z START RequestId",
		"\n14:02:29Z START RequestId",
		"Trimmed: showing the first 20 and the last 20 of 150 events.",
		"narrow the time window or the filter in params, or raise maxLines (up to 200)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "14:00:20Z") || strings.Contains(got, "14:02:09Z") {
		t.Errorf("an event from the trimmed middle was shown:\n%s", got)
	}
	for _, gone := range []string{`"eventId"`, "ingestionTime", "searchedLogStreams", "logStreamName"} {
		if strings.Contains(got, gone) {
			t.Errorf("the summary still carries %q", gone)
		}
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 1+20+1+20+1 {
		t.Errorf("got %d lines, want a header, 40 events, a gap and the trim note:\n%s", len(lines), got)
	}
}

func TestAHundredKilobyteLogReadStaysSmall(t *testing.T) {
	result := cloudwatchLogRead(t, 800, nil)
	full, _ := json.MarshalIndent(result, "", "  ")
	if len(full) < 100*1024 {
		t.Fatalf("fixture is only %d bytes; it should be the 100+ KB read this fixes", len(full))
	}
	got := RenderSignalRead("cloudwatch", "filterLogEvents", result, SignalReadOptions{})
	if len(got) > 16*1024 {
		t.Errorf("a %d-byte read rendered as %d bytes", len(full), len(got))
	}
}

func TestMaxLinesIsHonouredAndCapped(t *testing.T) {
	result := cloudwatchLogRead(t, 500, nil)

	got := RenderSignalRead("cloudwatch", "filterLogEvents", result, SignalReadOptions{MaxLines: 5})
	if !strings.Contains(got, "showing the first 3 and the last 2 of 500 events") {
		t.Errorf("maxLines 5:\n%s", got)
	}

	got = RenderSignalRead("cloudwatch", "filterLogEvents", result, SignalReadOptions{MaxLines: 100000})
	if !strings.Contains(got, "showing the first 100 and the last 100 of 500 events") {
		t.Errorf("maxLines must be capped at %d:\n%s", SignalLinesMax, got[len(got)-300:])
	}

	got = RenderSignalRead("cloudwatch", "filterLogEvents", cloudwatchLogRead(t, 12, nil), SignalReadOptions{})
	if strings.Contains(got, "Trimmed") || strings.Contains(got, "not shown") {
		t.Errorf("a read that fits must not claim it was trimmed:\n%s", got)
	}
}

func TestAPageTokenSaysHowToReadTheNextPage(t *testing.T) {
	result := cloudwatchLogRead(t, 3, map[string]any{"nextToken": "Bxkq6kVGFtq2y_MoigeqscPOdhXVbhiVtLoAmXb5jCrI7fXE"})
	got := RenderSignalRead("cloudwatch", "filterLogEvents", result, SignalReadOptions{})
	want := `The source has another page: repeat the call with params.nextToken set to "Bxkq6kVGFtq2y_MoigeqscPOdhXVbhiVtLoAmXb5jCrI7fXE".`
	if !strings.HasSuffix(got, want) {
		t.Errorf("got:\n%s", got)
	}
}

func TestALongLogLineIsCutAndFolded(t *testing.T) {
	result := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "filterLogEvents", "raw": map[string]any{
		"events": []any{map[string]any{"timestamp": signalsBase.UnixMilli(), "message": "a\n\tb " + strings.Repeat("x", 2000)}},
	}})
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	line := strings.Split(got, "\n")[1]
	if !strings.HasPrefix(line, "14:00:00Z a b xxx") || !strings.HasSuffix(line, "…") {
		t.Errorf("line = %q", line)
	}
	if n := len([]rune(line)); n > 320 {
		t.Errorf("one log line rendered as %d runes", n)
	}
	if !strings.HasSuffix(got, "\n1 line was cut at 300 characters; pass raw: true to read it in full.") {
		t.Errorf("a cut line must be said, with how to read it in full:\n%s", got)
	}
}

// A line cut to fit reads as the whole message unless the view says it was
// cut. Two CloudWatch events of about 1.2 KB each, the end of each one (the
// exception a stack trace ends with) past the cut.
func TestCutLinesAreCountedAndSaidSo(t *testing.T) {
	trace := func(i int) string {
		return fmt.Sprintf("ERROR checkout request %d failed\n", i) +
			strings.Repeat("\tat com.example.checkout.PaymentClient.authorize(PaymentClient.java:212)\n", 16) +
			"Caused by: java.net.SocketTimeoutException: Read timed out"
	}
	result := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "filterLogEvents", "raw": map[string]any{
		"events": []any{
			map[string]any{"timestamp": signalsBase.UnixMilli(), "message": trace(1)},
			map[string]any{"timestamp": signalsBase.Add(time.Second).UnixMilli(), "message": trace(2)},
			map[string]any{"timestamp": signalsBase.Add(2 * time.Second).UnixMilli(), "message": "short and whole"},
		},
	}})
	if n := len(trace(1)); n < 1100 {
		t.Fatalf("fixture line is only %d bytes", n)
	}
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	if strings.Contains(got, "SocketTimeoutException") {
		t.Fatalf("the fixture's tail should be past the cut:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n14:00:02Z short and whole\n2 lines were cut at 300 characters; pass raw: true to read them in full.") {
		t.Errorf("got:\n%s", got)
	}
	// raw: true is where the rest is, and it is really there.
	if raw := RenderSignalRead("", "", result, SignalReadOptions{Raw: true}); !strings.Contains(raw, "SocketTimeoutException") {
		t.Errorf("raw: true does not carry the cut tail")
	}

	// Beside a trim note and a page token, the cut note keeps its own line,
	// and the token stays last.
	long := cloudwatchLogRead(t, 60, map[string]any{"nextToken": "tok-2"})
	events := long["raw"].(map[string]any)["events"].([]any)
	events[0].(map[string]any)["message"] = strings.Repeat("y", 400)
	got = RenderSignalRead("", "", long, SignalReadOptions{})
	tail := got[strings.Index(got, "Trimmed:"):]
	lines := strings.Split(tail, "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "1 line was cut at 300 characters") ||
		!strings.HasPrefix(lines[2], "The source has another page") {
		t.Errorf("tail:\n%s", tail)
	}
}

func TestEveryViewSaysWhenItCutALine(t *testing.T) {
	text := roundTrip(t, map[string]any{"source": "mcp-server", "operation": "search_runbooks",
		"raw": map[string]any{"content": []any{map[string]any{"type": "text", "text": "short\n" + strings.Repeat("z", 500)}}}})
	if got := RenderSignalRead("", "", text, SignalReadOptions{}); !strings.HasSuffix(got,
		"\n1 line was cut at 300 characters; pass raw: true to read it in full.") {
		t.Errorf("text view:\n%s", got)
	}

	list := roundTrip(t, map[string]any{"source": "remote", "operation": "topology", "raw": map[string]any{
		"services": []any{map[string]any{"svc": strings.Repeat("a", 400)}, map[string]any{"svc": "payments"}},
		"note":     strings.Repeat("b", 400),
	}})
	if got := RenderSignalRead("", "", list, SignalReadOptions{}); !strings.HasSuffix(got,
		"\n2 lines were cut at 300 characters; pass raw: true to read them in full.") {
		t.Errorf("list view:\n%s", got)
	}

	series := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "getMetricStatistics", "raw": map[string]any{
		"Label":      strings.Repeat("n", 200),
		"Datapoints": []any{map[string]any{"Timestamp": "2026-09-28T14:00:00Z", "Average": 1.5}},
	}})
	if got := RenderSignalRead("", "", series, SignalReadOptions{}); !strings.HasSuffix(got,
		"\n1 series name was cut at 160 characters; pass raw: true to read it in full.") {
		t.Errorf("series view:\n%s", got)
	}

	// Nothing cut, nothing said.
	for _, whole := range []map[string]any{cloudwatchLogRead(t, 150, nil), cloudwatchLogRead(t, 3, nil)} {
		if got := RenderSignalRead("", "", whole, SignalReadOptions{}); strings.Contains(got, "was cut") || strings.Contains(got, "were cut") {
			t.Errorf("a read with no cut line claims one:\n%s", got)
		}
	}
}

func TestAReadSpanningDaysDatesEachLine(t *testing.T) {
	late := time.Date(2026, 9, 27, 23, 59, 58, 0, time.UTC)
	result := roundTrip(t, map[string]any{"raw": map[string]any{"events": []any{
		map[string]any{"timestamp": late.UnixMilli(), "message": "before midnight"},
		map[string]any{"timestamp": late.Add(4 * time.Second).UnixMilli(), "message": "after midnight"},
	}}, "source": "cloudwatch", "operation": "filterLogEvents"})
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	for _, want := range []string{
		"2 log events, 2026-09-27 23:59:58Z to 2026-09-28 00:00:02Z.",
		"\n09-27 23:59:58Z before midnight\n09-28 00:00:02Z after midnight",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestNoMatchingEventsIsSaidPlainly(t *testing.T) {
	result := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "filterLogEvents", "raw": map[string]any{"events": []any{}}})
	if got := RenderSignalRead("", "", result, SignalReadOptions{}); got != "cloudwatch filterLogEvents: no log events came back." {
		t.Errorf("got %q", got)
	}
}

func TestDatadogAndLokiLogsReadTheSameWay(t *testing.T) {
	datadog := decode(t, `{"source":"datadog","operation":"listLogs","raw":{"data":[
		{"id":"AAAA","type":"log","attributes":{"timestamp":"2026-09-28T14:00:05.120Z","message":"payment timeout","service":"checkout","status":"error"}},
		{"id":"BBBB","type":"log","attributes":{"timestamp":"2026-09-28T14:00:01.000Z","message":"retrying","service":"payments","status":"warn"}}
	],"meta":{"page":{"after":"eyJhZnRlciI6IkFRQUFBWE"}}}}`)
	got := RenderSignalRead("", "", datadog, SignalReadOptions{})
	for _, want := range []string{
		"datadog listLogs: 2 log events, 2026-09-28 14:00:01Z to 14:00:05Z, across 2 streams.",
		"\n14:00:01Z retrying\n14:00:05Z payment timeout",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("datadog: missing %q in:\n%s", want, got)
		}
	}

	loki := decode(t, `{"source":"loki","operation":"queryRange","raw":{"status":"success","data":{"resultType":"streams","result":[
		{"stream":{"app":"web","level":"error"},"values":[["1790604010000000000","GET /checkout 503"],["1790604000000000000","GET /cart 503"]]}
	]}}}`)
	got = RenderSignalRead("", "", loki, SignalReadOptions{})
	for _, want := range []string{
		"loki queryRange: 2 log events, 2026-09-28 14:00:00Z to 14:00:10Z.",
		"\n14:00:00Z GET /cart 503\n14:00:10Z GET /checkout 503",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("loki: missing %q in:\n%s", want, got)
		}
	}
}

func TestAMetricReadIsEachSeriesSummaryAndItsPoints(t *testing.T) {
	result := decode(t, `{"source":"cloudwatch","operation":"getMetricStatistics","raw":{"Label":"5xxErrorRate","Datapoints":[
		{"Timestamp":"2026-09-28T14:02:00.000Z","Average":3.2,"Unit":"Percent"},
		{"Timestamp":"2026-09-28T14:00:00.000Z","Average":0.1,"Unit":"Percent"},
		{"Timestamp":"2026-09-28T14:01:00.000Z","Average":12.4,"Unit":"Percent"}
	],"$metadata":{"httpStatusCode":200,"requestId":"4c0e6a4e-0000"}}}`)
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	want := "cloudwatch getMetricStatistics: 1 series, 3 points, 2026-09-28 14:00:00Z to 14:02:00Z.\n" +
		"5xxErrorRate (Percent): 3 points; min 0.1, max 12.4, last 3.2 at 14:02:00Z\n" +
		"  14:00:00Z 0.1\n  14:01:00Z 12.4\n  14:02:00Z 3.2"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMetricPointsShareTheLineBudget(t *testing.T) {
	stamps, values := []any{}, []any{}
	for i := 59; i >= 0; i-- { // GetMetricData answers newest first
		stamps = append(stamps, signalsBase.Add(time.Duration(i)*time.Minute).Format(time.RFC3339))
		values = append(values, float64(i))
	}
	result := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "getMetricData", "raw": map[string]any{
		"MetricDataResults": []any{
			map[string]any{"Id": "m1", "Label": "5xx", "Timestamps": stamps, "Values": values, "StatusCode": "Complete"},
			map[string]any{"Id": "m2", "Label": "", "Timestamps": stamps, "Values": values, "StatusCode": "Complete"},
		},
	}})
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	for _, want := range []string{
		"cloudwatch getMetricData: 2 series, 120 points, 2026-09-28 14:00:00Z to 14:59:00Z.",
		"5xx: 60 points; min 0, max 59, last 59 at 14:59:00Z\n  14:00:00Z 0\n",
		"  … 40 points not shown …\n",
		"m2: 60 points; min 0, max 59, last 59 at 14:59:00Z",
		"Trimmed: showing 40 of 120 points. To see more, narrow the time window, query fewer series, use a longer period (step) in params, or raise maxLines (up to 200).",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestManySeriesAreSummarisedNotListed(t *testing.T) {
	var result strings.Builder
	result.WriteString(`{"source":"prometheus","operation":"queryRange","raw":{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			result.WriteString(",")
		}
		fmt.Fprintf(&result, `{"metric":{"__name__":"http_requests_total","pod":"web-%02d"},"values":[[1790604000,"1"],[1790604060,"%d"]]}`, i, i)
	}
	result.WriteString(`]}}}`)
	got := RenderSignalRead("", "", decode(t, result.String()), SignalReadOptions{})
	for _, want := range []string{
		"prometheus queryRange: 60 series, 120 points, 2026-09-28 14:00:00Z to 14:01:00Z.",
		"http_requests_total{pod=web-07}: 2 points; min 1, max 7, last 7 at 14:01:00Z\n",
		"+10 more series not shown.",
		"Trimmed: points are not listed for 50 series sharing a 40-line budget",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "web-55") {
		t.Errorf("a series past the cap was shown")
	}
}

func TestDatadogSeriesReadWithTheirUnit(t *testing.T) {
	result := decode(t, `{"source":"datadog","operation":"queryMetrics","raw":{"status":"ok","series":[
		{"metric":"system.cpu.user","scope":"host:web-1","pointlist":[[1790604000000,12.5],[1790604060000,null],[1790604120000,80.25]],
		 "unit":[{"family":"percentage","name":"percent","short_name":"%"},null]}
	]}}`)
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	if !strings.Contains(got, "host:web-1 (%): 2 points; min 12.5, max 80.25, last 80.25 at 14:02:00Z") {
		t.Errorf("got:\n%s", got)
	}
}

func TestADegradedReadSaysSoAndIsNotARefusal(t *testing.T) {
	result := decode(t, `{"source":"cloudwatch","operation":"filterLogEvents","params":{},"fetchedAt":"2026-09-28T14:00:00Z",
		"partial":true,"error":"ThrottlingException: Rate exceeded"}`)
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	want := "cloudwatch filterLogEvents did not fully answer: ThrottlingException: Rate exceeded. The read was allowed; " +
		"the source itself failed or degraded, so retry, narrow the request, or try another operation."
	if got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestRawIsTheEnvelopeAndStillCapped(t *testing.T) {
	small := decode(t, `{"source":"cloudwatch","operation":"describeLogGroups","raw":{"logGroups":[{"logGroupName":"/aws/lambda/<checkout>"}]}}`)
	got := RenderSignalRead("", "", small, SignalReadOptions{Raw: true})
	if got != `{"operation":"describeLogGroups","raw":{"logGroups":[{"logGroupName":"/aws/lambda/<checkout>"}]},"source":"cloudwatch"}` {
		t.Errorf("small raw = %s", got)
	}

	big := cloudwatchLogRead(t, 800, nil)
	got = RenderSignalRead("", "", big, SignalReadOptions{Raw: true})
	body, note, found := strings.Cut(got, "\n\nTrimmed: ")
	if !found {
		t.Fatalf("a raw answer over the cap must say it was trimmed:\n%s", got[len(got)-200:])
	}
	if len(body) > SignalRawMaxBytes {
		t.Errorf("raw body is %d bytes, over the %d cap", len(body), SignalRawMaxBytes)
	}
	if !strings.HasPrefix(body, `{"fetchedAt":`) || !strings.Contains(note, "this shows its first 64 KB") ||
		!strings.Contains(note, "Narrow the time window or the filter in params") {
		t.Errorf("note = %q", note)
	}
}

func TestAnUnknownAnswerShowsItsMainListBounded(t *testing.T) {
	groups := []any{}
	for i := 0; i < 57; i++ {
		groups = append(groups, map[string]any{"logGroupName": fmt.Sprintf("/aws/lambda/fn-%02d", i), "storedBytes": i * 1000})
	}
	result := roundTrip(t, map[string]any{"source": "cloudwatch", "operation": "describeLogGroups",
		"raw": map[string]any{"logGroups": groups, "nextToken": "tok-2"}})
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	for _, want := range []string{
		"cloudwatch describeLogGroups: logGroups has 57 items.\n",
		`{"logGroupName":"/aws/lambda/fn-00","storedBytes":0}`,
		`{"logGroupName":"/aws/lambda/fn-39","storedBytes":39000}`,
		"Trimmed: showing 40 of 57 items.",
		`repeat the call with params.nextToken set to "tok-2".`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "fn-40") {
		t.Errorf("an item past maxLines was shown")
	}
}

// A payload that shares a field name with a log answer (Datadog's events API
// also answers with `events`) is not reported as "no log events".
func TestALookalikeIsNotMistakenForLogs(t *testing.T) {
	result := decode(t, `{"source":"datadog","operation":"listEvents","raw":{"events":[
		{"date_happened":1790604000,"title":"Deploy checkout v42","text":"by Dana","priority":"normal"}
	]}}`)
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	if strings.Contains(got, "log event") || !strings.Contains(got, "events has 1 item.") || !strings.Contains(got, "Deploy checkout v42") {
		t.Errorf("got:\n%s", got)
	}
}

func TestAnMCPSourcesTextIsReadThroughToItsShape(t *testing.T) {
	inner := `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"app":"web"},"values":[["1790604000000000000","GET /checkout 503"]]}]}}`
	result := roundTrip(t, map[string]any{"source": "grafana", "operation": "query_loki_logs",
		"raw": map[string]any{"content": []any{map[string]any{"type": "text", "text": inner}}}})
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	if !strings.Contains(got, "grafana query_loki_logs: 1 log event, at 2026-09-28 14:00:00Z.\n14:00:00Z GET /checkout 503") {
		t.Errorf("got:\n%s", got)
	}

	plain := roundTrip(t, map[string]any{"source": "grafana", "operation": "list_dashboards",
		"raw": map[string]any{"content": []any{map[string]any{"type": "text", "text": "checkout overview\npayments"}}}})
	if got := RenderSignalRead("", "", plain, SignalReadOptions{}); got != "grafana list_dashboards: 2 lines of text.\ncheckout overview\npayments" {
		t.Errorf("got %q", got)
	}
}

func TestNothingBackIsSaidPlainly(t *testing.T) {
	if got := RenderSignalRead("datadog", "metrics.range", nil, SignalReadOptions{}); got != "datadog metrics.range: the source returned no data." {
		t.Errorf("nil: %q", got)
	}
	got := RenderSignalRead("", "", decode(t, `{"source":"loki","operation":"queryRange","raw":{}}`), SignalReadOptions{})
	if got != "loki queryRange: the source returned no data." {
		t.Errorf("empty raw: %q", got)
	}
}

func TestSignalTimeReadsEveryEpochUnit(t *testing.T) {
	want := signalsBase
	for _, v := range []any{
		float64(want.Unix()), float64(want.UnixMilli()), float64(want.UnixMicro()), "1790604000000000000",
		"2026-09-28T14:00:00Z", "2026-09-28T14:00:00.000Z", "2026-09-28T14:00:00.0000000Z", "1790604000",
	} {
		got, ok := signalTime(v)
		if !ok || !got.Equal(want) {
			t.Errorf("signalTime(%v) = %v, %v", v, got, ok)
		}
	}
	for _, v := range []any{"", "soon", float64(-1), nil, true, "1e9"} {
		if _, ok := signalTime(v); ok {
			t.Errorf("signalTime(%v) read a time", v)
		}
	}
}

func TestNumbersReadAsAPersonWritesThem(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 42: "42", 12.5: "12.5", 0.1: "0.1", 3.14159265: "3.1416", 0.00012: "0.00012", -7: "-7"} {
		if got := number(in); got != want {
			t.Errorf("number(%v) = %q, want %q", in, got, want)
		}
	}
	if got := plural(1284, "event", "events"); got != "1,284 events" {
		t.Errorf("plural = %q", got)
	}
}

func TestANestedListIsFoundAndNotRepeated(t *testing.T) {
	result := decode(t, `{"source":"remote","operation":"topology","raw":{"status":"ok","data":{"result":[{"svc":"checkout"},{"svc":"payments"}],"took":12}}}`)
	got := RenderSignalRead("", "", result, SignalReadOptions{})
	want := "remote topology: data.result has 2 items.\n{\"svc\":\"checkout\"}\n{\"svc\":\"payments\"}\nstatus: ok"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
