// The openresty route factory's tuning knobs, as the deploy form offers them.
//
// They reach openresty untouched by any chart: swiss writes them into
// modelRoute.nginx.values, the sglang chart renders that map verbatim ("the key
// set belongs to openresty's autoconfig, not to this chart"), and autoconfig's
// luaVal writes each one into the lua route table -- numbers and booleans
// unquoted, everything else as a string. So a knob openresty gains needs no
// change here either: the Custom option covers it until it is worth a row.
//
// Defaults below are openresty's own, from lua/route.lua and session_base.conf.
// They are shown, never sent: an empty field omits the key and lets openresty
// answer, which is the only way the two stay in step when its defaults move.

export type DirectiveKind = "number" | "boolean" | "text";

export interface Directive {
  key: string;
  label: string;
  group: string;
  kind: DirectiveKind;
  // openresty's default, for the placeholder. A sentence fragment is fine
  // where the default is derived rather than a literal.
  fallback: string;
  detail: string;
  // Only meaningful while adaptive concurrency is on, which is most of the
  // AIMD family: openresty reads them, but nothing moves without the loop.
  needsAdaptive?: boolean;
}

export const DIRECTIVES: Directive[] = [
  {
    key: "adaptive_cc_min",
    label: "Adaptive floor",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "derived from the fraction",
    detail:
      "Absolute floor for the AIMD limit, in requests. Wins over the fraction: openresty derives a floor from the static peer max only when this is unset. Must be at least 1; anything less is logged and ignored.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_inc",
    label: "Climb multiplier",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "1.02",
    detail:
      "Applied per step when the decode rate is healthy and concurrency is pressing the limit. Recovery is deliberately slower than the shrink.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_dec",
    label: "Shrink multiplier",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "0.97",
    detail: "Applied per step when the measured rate is below the threshold. Protection is fast, recovery slow.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_abs",
    label: "Absolute headroom",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "5",
    detail:
      "Slots the limit is kept above live concurrency, whatever the fractions say. Without it the relative band is too narrow at small concurrency, where one request flips the state and a burst has no buffer.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_pressure_frac",
    label: "Pressure fraction",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "0.9",
    detail:
      "The limit only climbs when live concurrency reaches this fraction of it -- otherwise light traffic walks the limit up to the maximum and a burst lands all at once. Must be over 0 and at most 1.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_slack_frac",
    label: "Slack fraction",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "0.7",
    detail:
      "Below this fraction the limit follows concurrency back down, so it does not stay parked at yesterday's peak. Must be over 0 and strictly under the pressure fraction, or the climb and shrink gates fight.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_ttl",
    label: "Limit TTL",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "300",
    detail:
      "Seconds the computed limit survives without a signal. Past it the limit ages out and the route slow-starts from the floor again -- which a quiet route will do repeatedly.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_interval",
    label: "Step interval",
    group: "Adaptive concurrency",
    kind: "number",
    fallback: "the TPS window",
    detail: "Seconds between AIMD steps. Defaults to the TPS aggregation window, which is what the signal is measured over.",
    needsAdaptive: true,
  },
  {
    key: "adaptive_cc_use_ttft",
    label: "Judge on TTFT too",
    group: "Adaptive concurrency",
    kind: "boolean",
    fallback: "on",
    detail:
      "Whether a TTFT violation also shrinks the limit, or only the decode rate does. Off is for a route whose time to first token is naturally long -- prompts are big -- and which would otherwise shrink forever.",
    needsAdaptive: true,
  },

  {
    key: "default_max",
    label: "Default peer maximum",
    group: "Capacity",
    kind: "number",
    fallback: "20",
    detail: "In-flight requests allowed per peer when the peer entry names no maximum of its own.",
  },
  {
    key: "rt_limit_factor",
    label: "Runtime limit factor",
    group: "Capacity",
    kind: "number",
    fallback: "1",
    detail: "Scales the effective per-peer limit at runtime. Must be over 0; anything else is logged and the global default stands.",
  },
  {
    key: "max_more_tries",
    label: "Retry budget",
    group: "Capacity",
    kind: "number",
    fallback: "2",
    detail: "Upper bound on balancer retries for one request. A positive integer; anything else falls back to 2.",
  },
  {
    key: "cross_tier_fallback",
    label: "Cross-tier fallback",
    group: "Capacity",
    kind: "boolean",
    fallback: "off",
    detail:
      "On, a 5xx or connection failure in the top tier falls straight through to the tier below instead of waiting for the health timer to ban the peer.",
  },

  {
    key: "health_check_interval",
    label: "Probe interval",
    group: "Health checks",
    kind: "number",
    fallback: "10",
    detail: "Seconds between active probes of each peer.",
  },
  {
    key: "health_ban_ttl",
    label: "Ban TTL",
    group: "Health checks",
    kind: "number",
    fallback: "300",
    detail: "Seconds a failing peer stays banned before it is probed back in.",
  },
  {
    key: "health_probe_path",
    label: "Probe path",
    group: "Health checks",
    kind: "text",
    fallback: "/v1/models",
    detail:
      "Path the probe GETs. Worth overriding when a peer is itself a router, whose /v1/models answers 200 while every worker behind it is down.",
  },
  {
    key: "cluster_avg_interval",
    label: "Cluster average interval",
    group: "Health checks",
    kind: "number",
    fallback: "30",
    detail: "Seconds between cluster-average aggregation passes.",
  },

  {
    key: "ttft_ewma_alpha",
    label: "TTFT EWMA alpha",
    group: "TTFT measurement",
    kind: "number",
    fallback: "0.3",
    detail: "Weight of the newest sample in the TTFT moving average. Higher reacts faster and is noisier.",
  },
  {
    key: "ttft_window",
    label: "TTFT window",
    group: "TTFT measurement",
    kind: "number",
    fallback: "20",
    detail: "Seconds the TTFT histogram aggregates over.",
  },
  {
    key: "ttft_ttl",
    label: "TTFT TTL",
    group: "TTFT measurement",
    kind: "number",
    fallback: "60",
    detail: "Seconds a TTFT reading stays current before it is treated as no signal.",
  },
  {
    key: "ttft_probe_window",
    label: "TTFT probe window",
    group: "TTFT measurement",
    kind: "number",
    fallback: "10",
    detail: "Seconds over which the probe allowance below is counted.",
  },
  {
    key: "ttft_probe_per_window",
    label: "TTFT probes per window",
    group: "TTFT measurement",
    kind: "number",
    fallback: "5",
    detail: "How many requests per window are measured for TTFT. Measurement is sampled, not universal.",
  },

  {
    key: "tps_ewma_alpha",
    label: "TPS EWMA alpha",
    group: "TPS measurement",
    kind: "number",
    fallback: "0.3",
    detail: "Weight of the newest sample in the decode-rate moving average.",
  },
  {
    key: "tps_window",
    label: "TPS window",
    group: "TPS measurement",
    kind: "number",
    fallback: "20",
    detail:
      "Seconds the decode-rate histogram aggregates over, and the AIMD step interval unless that is set separately. A low tail has few samples, so a wider window is steadier.",
  },
  {
    key: "tps_ttl",
    label: "TPS TTL",
    group: "TPS measurement",
    kind: "number",
    fallback: "60",
    detail: "Seconds a decode-rate reading stays current before it counts as no signal.",
  },
  {
    key: "tps_probe_window",
    label: "TPS probe window",
    group: "TPS measurement",
    kind: "number",
    fallback: "10",
    detail: "Seconds over which the probe allowance below is counted.",
  },
  {
    key: "tps_probe_per_window",
    label: "TPS probes per window",
    group: "TPS measurement",
    kind: "number",
    fallback: "5",
    detail: "How many requests per window are measured for decode rate.",
  },
  {
    key: "tps_min_tokens",
    label: "Minimum tokens",
    group: "TPS measurement",
    kind: "number",
    fallback: "16",
    detail: "Completions shorter than this are not counted: a handful of tokens says nothing about decode rate.",
  },
  {
    key: "tps_min_decode_s",
    label: "Minimum decode seconds",
    group: "TPS measurement",
    kind: "number",
    fallback: "0.5",
    detail: "Completions decoded faster than this are not counted, for the same reason.",
  },

  {
    key: "reject_rules_status",
    label: "Rejection status",
    group: "Other",
    kind: "number",
    fallback: "429",
    detail: "HTTP status returned when a content rejection rule matches.",
  },
  {
    key: "bodylog_default_pct",
    label: "Body log sample percent",
    group: "Other",
    kind: "number",
    fallback: "openresty's global",
    detail: "Percentage of request bodies sampled into the body log.",
  },
];

// Written by the SLO plane, from the LLMSLORequirement. They are rendered after
// this map, so a hand-written copy here is silently overwritten -- and a nested
// table cannot survive luaVal anyway: it arrives as a string and the engine
// drops the lot.
export const SLO_OWNED = ["ttft_metrics", "tps_metrics"];

// Accepted by the CRD only on a video route. On an LLM route the CEL rule
// refuses the whole object, so the deploy fails at apply rather than here.
export const VIDEO_ONLY = [
  "max_body_size",
  "proxy_timeout",
  "connect_timeout",
  "rate_limit",
  "upload_conn_limit",
  "upload_req_limit",
  "upload_req_burst",
  "upload_limit_key",
  "download_conn_limit",
  "auth",
  "auth_public_paths",
];

// The CRD caps the map at 32 keys -- a CEL cost budget, not tidiness -- and the
// named rows below spend some of it.
export const MAX_VALUES = 32;

export const DIRECTIVES_BY_KEY = new Map(DIRECTIVES.map((d) => [d.key, d]));

export function directiveGroups(): string[] {
  return [...new Set(DIRECTIVES.map((d) => d.group))];
}

// Why a key cannot be used, or "" when it can. Custom keys are the point of a
// free-form map, so this rejects only what the chain below will refuse or
// discard -- never a key merely because it is unknown here.
export function rejectKey(key: string, taken: string[]): string {
  const k = key.trim();
  if (!k) return "A key is required.";
  if (!/^[a-z][a-z0-9_]*$/.test(k)) return "Lowercase letters, digits and underscores only.";
  if (taken.includes(k)) return "Already set on this route.";
  if (SLO_OWNED.includes(k)) return "Written from the SLO requirement; a value here is silently overwritten.";
  if (VIDEO_ONLY.includes(k)) return "Only valid on a video route; the CRD refuses it on this one.";
  return "";
}
