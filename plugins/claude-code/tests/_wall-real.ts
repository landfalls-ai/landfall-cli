// What `landfall wall --host claude-code` answered for the live room of the 2026-10-09 desktop run
// (collab-harness cascade): seven widgets, the pinned chart last, and then a stat that landed on
// the shared wall and the CLI put BEFORE the pinned one. Captured, not made up.
export const WALL_BEFORE = {
 "ok": true,
 "people": [
  {
   "artifacts": 0,
   "displayName": "collab-bob",
   "edgeAgentLabel": "bob-claude-desktop",
   "humanActorId": "01a09ba6-00b0-7672-8259-074b88a9f776",
   "kind": "member",
   "latestSeq": 77,
   "trail": 0,
   "widgets": 1
  },
  {
   "artifacts": 0,
   "displayName": "collab-alice",
   "edgeAgentLabel": "Claude Code",
   "humanActorId": "01a09ba5-ffa0-7e11-8e63-5b787a426631",
   "kind": "member",
   "latestSeq": 59,
   "trail": 0,
   "widgets": 2
  },
  {
   "artifacts": 0,
   "displayName": "collab-carol",
   "edgeAgentLabel": "carol-war-room",
   "humanActorId": "01a09ba6-0196-7610-b5ae-880702d76a9b",
   "kind": "member",
   "latestSeq": 48,
   "trail": 0,
   "widgets": 1
  }
 ],
 "unavailable": [],
 "windowMs": 2801913,
 "widgets": [
  {
   "id": "w-status",
   "lines": [
    {
     "level": "",
     "text": "Phase: concluded"
    },
    {
     "level": "",
     "text": "Findings so far: 0"
    },
    {
     "level": "",
     "text": "Updated 17:15:51Z"
    }
   ],
   "seq": 5,
   "title": "Investigation status",
   "type": "logView"
  },
  {
   "id": "w-remediation",
   "lines": [
    {
     "level": "",
     "text": "## FAST FIX [temporary]"
    },
    {
     "level": "",
     "text": "No safe fast-stabilization action found for the current event."
    },
    {
     "level": "",
     "text": "by diagnosis-agent \u00b7 evidence: insufficient evidence"
    },
    {
     "level": "",
     "text": ""
    },
    {
     "level": "",
     "text": "## ROOT-CAUSE MITIGATION"
    },
    {
     "level": "",
     "text": "Root cause: not yet determined"
    },
    {
     "level": "",
     "text": "by diagnosis-agent \u00b7 evidence: insufficient evidence"
    },
    {
     "level": "",
     "text": "Updated 17:15:51Z"
    }
   ],
   "seq": 9,
   "title": "Remediation \u2014 fast fix + durable mitigation",
   "type": "logView"
  },
  {
   "id": "w-5xx-error-rate-global-4b077ef3",
   "seq": 31,
   "series": [
    {
     "label": "5xxErrorRate",
     "points": [
      [
       1791564130000,
       0.6836111111111111
      ],
      [
       1791564190000,
       9.626944444444444
      ],
      [
       1791564250000,
       16.171
      ],
      [
       1791564310000,
       20.029666666666667
      ],
      [
       1791564370000,
       22.241666666666667
      ],
      [
       1791564430000,
       23.464666666666666
      ],
      [
       1791564490000,
       24.109333333333332
      ],
      [
       1791564550000,
       0.2775
      ],
      [
       1791564610000,
       0.26333333333333336
      ],
      [
       1791564670000,
       0.2775
      ],
      [
       1791564730000,
       7.951428571428571
      ],
      [
       1791564790000,
       18.124333333333333
      ],
      [
       1791564850000,
       23.608333333333334
      ],
      [
       1791564910000,
       26.951666666666668
      ],
      [
       1791564970000,
       28.705416666666665
      ],
      [
       1791565030000,
       29.360416666666666
      ],
      [
       1791565090000,
       30.008750000000003
      ],
      [
       1791565150000,
       20.410833333333333
      ],
      [
       1791565210000,
       0.29
      ],
      [
       1791565270000,
       0.2816666666666667
      ],
      [
       1791565330000,
       0.285
      ],
      [
       1791565390000,
       0.2816666666666667
      ],
      [
       1791565450000,
       0.2916666666666667
      ],
      [
       1791565510000,
       0.2916666666666667
      ],
      [
       1791565570000,
       0.27166666666666667
      ],
      [
       1791565630000,
       0.29333333333333333
      ],
      [
       1791565690000,
       0.26666666666666666
      ],
      [
       1791565750000,
       0.2783333333333333
      ],
      [
       1791565870000,
       0.275
      ],
      [
       1791565930000,
       3.201388888888889
      ],
      [
       1791565990000,
       17.55333333333333
      ],
      [
       1791566050000,
       27.96388888888889
      ],
      [
       1791566110000,
       34.37111111111111
      ],
      [
       1791566170000,
       36.87222222222223
      ],
      [
       1791566230000,
       38.67055555555556
      ],
      [
       1791566290000,
       40.30111111111111
      ],
      [
       1791566350000,
       40.474444444444444
      ],
      [
       1791566410000,
       40.75416666666667
      ]
     ]
    }
   ],
   "title": "5xx Error Rate (Global)",
   "type": "chart"
  },
  {
   "id": "w-harness-main-chart",
   "markers": [
    {
     "atMs": 1791565909888,
     "label": "network event starts"
    }
   ],
   "seq": 36,
   "series": [
    {
     "label": "us-east-1a",
     "points": [
      [
       1791564802000,
       0
      ],
      [
       1791564862000,
       0
      ],
      [
       1791564922000,
       0
      ],
      [
       1791564982000,
       0
      ],
      [
       1791565042000,
       0
      ],
      [
       1791565102000,
       0
      ],
      [
       1791565162000,
       0
      ],
      [
       1791565222000,
       0
      ],
      [
       1791565282000,
       0
      ],
      [
       1791565342000,
       0
      ],
      [
       1791565402000,
       0
      ],
      [
       1791565462000,
       0
      ],
      [
       1791565522000,
       0
      ],
      [
       1791565582000,
       0
      ],
      [
       1791565642000,
       0
      ],
      [
       1791565702000,
       0
      ],
      [
       1791565762000,
       0
      ],
      [
       1791565822000,
       0
      ],
      [
       1791565882000,
       0
      ]
     ],
     "unit": "Bytes"
    },
    {
     "label": "us-east-1b",
     "points": [
      [
       1791564802000,
       9636925
      ],
      [
       1791564862000,
       9807664
      ],
      [
       1791564922000,
       9270880
      ],
      [
       1791564982000,
       9855525
      ],
      [
       1791565042000,
       9309337
      ],
      [
       1791565102000,
       0
      ],
      [
       1791565162000,
       0
      ],
      [
       1791565222000,
       0
      ],
      [
       1791565282000,
       0
      ],
      [
       1791565342000,
       0
      ],
      [
       1791565402000,
       0
      ],
      [
       1791565462000,
       0
      ],
      [
       1791565522000,
       0
      ],
      [
       1791565582000,
       0
      ],
      [
       1791565642000,
       0
      ],
      [
       1791565702000,
       0
      ],
      [
       1791565762000,
       0
      ],
      [
       1791565822000,
       0
      ],
      [
       1791565882000,
       0
      ]
     ],
     "unit": "Bytes"
    },
    {
     "label": "us-east-1c",
     "points": [
      [
       1791564802000,
       27035115
      ],
      [
       1791564862000,
       27135944
      ],
      [
       1791564922000,
       27908432
      ],
      [
       1791564982000,
       27314677
      ],
      [
       1791565042000,
       25723482
      ],
      [
       1791565102000,
       25720124
      ],
      [
       1791565162000,
       27448466
      ],
      [
       1791565222000,
       27857417
      ],
      [
       1791565282000,
       28229704
      ],
      [
       1791565342000,
       28287557
      ],
      [
       1791565402000,
       28681098
      ],
      [
       1791565462000,
       28290504
      ],
      [
       1791565522000,
       27366596
      ],
      [
       1791565582000,
       27851834
      ],
      [
       1791565642000,
       29880109
      ],
      [
       1791565702000,
       28507091
      ],
      [
       1791565762000,
       28250968
      ],
      [
       1791565822000,
       28557033
      ],
      [
       1791565882000,
       23008574
      ]
     ],
     "unit": "Bytes"
    }
   ],
   "title": "NAT bytes out by zone",
   "type": "chart"
  },
  {
   "id": "w-harness-main-logs",
   "lines": [
    {
     "at": "2026-10-09T16:45:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1a az=us-east-1a window=5m authorized=121 acquirer_p99=198ms errors=0"
    },
    {
     "at": "2026-10-09T16:50:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1b az=us-east-1b window=5m authorized=118 acquirer_p99=196ms errors=0"
    },
    {
     "at": "2026-10-09T16:55:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1c az=us-east-1c window=5m authorized=118 acquirer_p99=196ms errors=0"
    },
    {
     "at": "2026-10-09T17:00:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1a az=us-east-1a window=5m authorized=118 acquirer_p99=195ms errors=0"
    },
    {
     "at": "2026-10-09T17:05:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1b az=us-east-1b window=5m authorized=120 acquirer_p99=197ms errors=0"
    },
    {
     "at": "2026-10-09T17:10:34.000Z",
     "level": "info",
     "text": "INFO payments task=payments-1c az=us-east-1c window=5m authorized=123 acquirer_p99=200ms errors=0"
    },
    {
     "at": "2026-10-09T17:12:25.300Z",
     "level": "error",
     "text": "ERROR payments task=payments-1a az=us-east-1a authorize order=ord-54458 failed: connect ETIMEDOUT 203.0.113.40:443 (acquirer.cardnet.example) after 10000ms attempt=3/3 src=10.20.1."
    },
    {
     "at": "2026-10-09T17:12:25.750Z",
     "level": "error",
     "text": "ERROR payments task=payments-1b az=us-east-1b authorize order=ord-56365 failed: connect ETIMEDOUT 203.0.113.40:443 (acquirer.cardnet.example) after 10000ms attempt=3/3 src=10.20.2."
    },
    {
     "at": "2026-10-09T17:13:05.300Z",
     "level": "error",
     "text": "ERROR payments task=payments-1a az=us-east-1a authorize order=ord-56780 failed: connect ETIMEDOUT 203.0.113.40:443 (acquirer.cardnet.example) after 10000ms attempt=3/3 src=10.20.1."
    },
    {
     "at": "2026-10-09T17:13:05.750Z",
     "level": "error",
     "text": "ERROR payments task=payments-1b az=us-east-1b authorize order=ord-51841 failed: connect ETIMEDOUT 203.0.113.40:443 (acquirer.cardnet.example) after 10000ms attempt=3/3 src=10.20.2."
    },
    {
     "at": "2026-10-09T17:13:06.200Z",
     "level": "error",
     "text": "ERROR payments task=payments-1c az=us-east-1c authorize order=ord-55122 failed: connect ETIMEDOUT 203.0.113.40:443 (acquirer.cardnet.example) after 10000ms attempt=3/3 src=10.20.3."
    }
   ],
   "seq": 38,
   "title": "Payments timeouts",
   "type": "logView"
  },
  {
   "edges": [
    {
     "from": "cloudfront",
     "to": "alb",
     "trust": "inferred"
    },
    {
     "from": "alb",
     "to": "checkout",
     "trust": "inferred"
    },
    {
     "from": "checkout",
     "to": "payments",
     "tone": "critical",
     "trust": "inferred"
    },
    {
     "from": "payments",
     "to": "nat-1a",
     "tone": "critical",
     "trust": "inferred"
    },
    {
     "from": "payments",
     "to": "nat-1b",
     "trust": "inferred"
    },
    {
     "from": "payments",
     "to": "nat-1c",
     "trust": "inferred"
    },
    {
     "from": "nat-1a",
     "to": "acquirer",
     "tone": "critical",
     "trust": "inferred"
    },
    {
     "from": "nat-1b",
     "to": "acquirer",
     "trust": "inferred"
    },
    {
     "from": "nat-1c",
     "to": "acquirer",
     "trust": "inferred"
    },
    {
     "from": "checkout",
     "to": "db",
     "tone": "serious",
     "trust": "inferred"
    },
    {
     "from": "inventory",
     "to": "db",
     "trust": "inferred"
    },
    {
     "from": "orders",
     "to": "db",
     "trust": "inferred"
    }
   ],
   "focus": "payments",
   "id": "w-harness-main-graph",
   "nodes": [
    {
     "id": "cloudfront",
     "kind": "cdn",
     "label": "CloudFront E123ABC",
     "tone": "warning"
    },
    {
     "id": "alb",
     "kind": "load-balancer",
     "label": "ALB web-edge",
     "tone": "warning"
    },
    {
     "id": "checkout",
     "kind": "service",
     "label": "checkout (6 hosts)",
     "tone": "critical"
    },
    {
     "id": "payments",
     "kind": "service",
     "label": "payments",
     "tone": "critical"
    },
    {
     "id": "nat-1a",
     "kind": "nat-gateway",
     "label": "NAT 1a",
     "tone": "serious"
    },
    {
     "id": "nat-1b",
     "kind": "nat-gateway",
     "label": "NAT 1b",
     "tone": "neutral"
    },
    {
     "id": "nat-1c",
     "kind": "nat-gateway",
     "label": "NAT 1c",
     "tone": "neutral"
    },
    {
     "id": "acquirer",
     "kind": "external",
     "label": "card acquirer",
     "tone": "neutral"
    },
    {
     "id": "db",
     "kind": "database",
     "label": "checkout-db",
     "tone": "serious"
    },
    {
     "id": "inventory",
     "kind": "service",
     "label": "inventory",
     "tone": "warning"
    },
    {
     "id": "orders",
     "kind": "service",
     "label": "orders-worker",
     "tone": "warning"
    }
   ],
   "seq": 40,
   "title": "Egress path topology",
   "type": "graph"
  },
  {
   "id": "edge-widget-51",
   "pinned": true,
   "seq": 61,
   "series": [
    {
     "label": "nat-web-edge-1a",
     "points": [
      [
       1791564904000,
       0
      ],
      [
       1791564964000,
       0
      ],
      [
       1791565024000,
       0
      ],
      [
       1791565084000,
       0
      ],
      [
       1791565144000,
       0
      ],
      [
       1791565204000,
       0
      ],
      [
       1791565264000,
       0
      ],
      [
       1791565324000,
       0
      ],
      [
       1791565384000,
       0
      ],
      [
       1791565444000,
       0
      ],
      [
       1791565504000,
       0
      ],
      [
       1791565564000,
       0
      ],
      [
       1791565624000,
       0
      ],
      [
       1791565684000,
       0
      ],
      [
       1791565744000,
       0
      ],
      [
       1791565804000,
       0
      ],
      [
       1791565864000,
       0
      ],
      [
       1791565924000,
       0
      ],
      [
       1791565984000,
       0
      ]
     ],
     "unit": "Count"
    },
    {
     "label": "nat-web-edge-1b",
     "points": [
      [
       1791564904000,
       79135
      ],
      [
       1791564964000,
       82750
      ],
      [
       1791565024000,
       83730
      ],
      [
       1791565084000,
       0
      ],
      [
       1791565144000,
       0
      ],
      [
       1791565204000,
       0
      ],
      [
       1791565264000,
       0
      ],
      [
       1791565324000,
       0
      ],
      [
       1791565384000,
       0
      ],
      [
       1791565444000,
       0
      ],
      [
       1791565504000,
       0
      ],
      [
       1791565564000,
       0
      ],
      [
       1791565624000,
       0
      ],
      [
       1791565684000,
       0
      ],
      [
       1791565744000,
       0
      ],
      [
       1791565804000,
       0
      ],
      [
       1791565864000,
       0
      ],
      [
       1791565924000,
       0
      ],
      [
       1791565984000,
       0
      ]
     ],
     "unit": "Count"
    },
    {
     "label": "nat-web-edge-1c",
     "points": [
      [
       1791564904000,
       0
      ],
      [
       1791564964000,
       0
      ],
      [
       1791565024000,
       0
      ],
      [
       1791565084000,
       0
      ],
      [
       1791565144000,
       0
      ],
      [
       1791565204000,
       0
      ],
      [
       1791565264000,
       0
      ],
      [
       1791565324000,
       0
      ],
      [
       1791565384000,
       0
      ],
      [
       1791565444000,
       0
      ],
      [
       1791565504000,
       0
      ],
      [
       1791565564000,
       0
      ],
      [
       1791565624000,
       0
      ],
      [
       1791565684000,
       0
      ],
      [
       1791565744000,
       0
      ],
      [
       1791565804000,
       0
      ],
      [
       1791565864000,
       30406
      ],
      [
       1791565924000,
       81464
      ],
      [
       1791565984000,
       81575
      ]
     ],
     "unit": "Count"
    }
   ],
   "title": "NAT packet drops by gateway",
   "type": "chart"
  }
 ]
}
export const ARRIVAL = {
 "id": "w-probe-1",
 "seq": 79,
 "title": "Probe widget one",
 "type": "stat"
}
