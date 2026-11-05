// The shapes the engine reports. Latencies are nanoseconds so the choice of
// unit is made here rather than baked into the wire format.
export type State = "idle" | "running" | "done" | "failed";

export interface Exchange {
  name: string;
  sent: number;
  ok: number;
  rejected: number;
  retransmits: number;
  timeouts: number;
  unverified: number;
  blocked: number;
  p50: number;
  p95: number;
  p99: number;
  p999: number;
  max: number;
  mean: number;
  serviceP50: number;
  serviceP95: number;
}

export interface DynAuth {
  disconnectRequests: number;
  disconnectAcks: number;
  disconnectNaks: number;
  coaRequests: number;
  coaAcks: number;
  coaNaks: number;
  unverified: number;
  malformed: number;
}

export interface Sample {
  t: number;
  offered: number;
  answered: number;
  lost: number;
  p95: number;
  service: number;
}

export interface Status {
  state: State;
  config: Record<string, unknown>;
  error?: string;
  startedAt?: string;
  elapsedMs: number;
  exchanges: Exchange[] | null;
  dynAuth: DynAuth;
  sessionsStarted: number;
  sessionsCompleted: number;
  sessionsOnline: number;
  outstanding: number;
  setupRate: number;
  answerRate: number;
  lossRate: number;
  offeredRate: number;
  achievedRate: number;
  links: number;
  history?: Sample[];
  notes?: string[];
}

export interface Attr {
  name: string;
  value: string;
}

// RunConfig mirrors what the server accepts. The credentials are write-only:
// they are sent when a run starts and never come back.
export interface RunConfig {
  server: string;
  acctServer: string;
  secret: string;
  password: string;
  auth: string;
  username: string;

  rate: string;
  count: string;
  duration: string;
  hold: string;
  interim: string;

  shape: string;
  burstPeriod: string;
  burstLow: string;
  rejectRate: string;

  trafficUp: string;
  trafficUpMax: string;
  trafficDown: string;
  trafficDownMax: string;

  links: string;
  timeout: string;
  retries: string;

  nasIdentifier: string;
  nasIPAddress: string;
  nasPortType: string;
  nasPortId: string;
  callingStationId: string;

  attributes: Attr[];
  coaListen: string;
  seed: string;
}

export const blankConfig: RunConfig = {
  server: "", acctServer: "", secret: "", password: "", auth: "pap", username: "",
  rate: "", count: "", duration: "", hold: "", interim: "",
  shape: "steady", burstPeriod: "", burstLow: "", rejectRate: "",
  trafficUp: "", trafficUpMax: "", trafficDown: "", trafficDownMax: "",
  links: "", timeout: "", retries: "2",
  nasIdentifier: "", nasIPAddress: "", nasPortType: "", nasPortId: "", callingStationId: "",
  attributes: [], coaListen: "", seed: "",
};

const NUMERIC = new Set([
  "rate", "count", "trafficUp", "trafficUpMax", "trafficDown", "trafficDownMax",
  "links", "retries", "seed", "nasPortType", "rejectRate", "burstLow",
]);

// Empty fields are left out entirely so the server applies its own defaults
// rather than being told zero, which means something different.
export function toWire(cfg: RunConfig): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(cfg)) {
    if (k === "attributes") {
      const attrs = (v as Attr[]).filter((a) => a.name.trim() !== "");
      if (attrs.length) out[k] = attrs;
      continue;
    }
    const s = String(v ?? "").trim();
    if (!s) continue;
    out[k] = NUMERIC.has(k) ? Number(s) : s;
  }
  if (out.retries === undefined) out.retries = 0;
  return out;
}

// fromWire turns a saved profile back into form values, leaving credentials
// blank because a profile never carries them.
export function fromWire(v: Record<string, unknown>): RunConfig {
  const out = { ...blankConfig };
  for (const key of Object.keys(blankConfig) as (keyof RunConfig)[]) {
    if (key === "attributes") continue;
    const got = v[key];
    if (got === undefined || got === null || got === "") continue;
    (out[key] as string) = String(got);
  }
  out.attributes = Array.isArray(v.attributes) ? (v.attributes as Attr[]) : [];
  out.secret = "";
  out.password = "";
  if (!out.auth) out.auth = "pap";
  if (!out.shape) out.shape = "steady";
  return out;
}

export class ApiError extends Error {}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    throw new ApiError(`the server replied with something unreadable (HTTP ${res.status})`);
  }
  if (!res.ok) {
    const err = (body as { error?: string }).error;
    // HTTP/2 has no reason phrase, so never rely on statusText being useful.
    throw new ApiError(err || `request failed (HTTP ${res.status})`);
  }
  return body as T;
}

export interface AttrMatch {
  name: string;
  vendor?: number;
  type: number;
  data: number;
  values?: string[];
}

export interface DictionaryInfo {
  paths: string[];
  attributes: number;
  vendors: number;
}

export interface ProfileList {
  profiles: string[];
  dir: string;
}

export const api = {
  status: () => request<Status>("api/status"),
  dictionary: () => request<DictionaryInfo>("api/dictionary"),
  attributes: (q: string) =>
    request<{ matches: AttrMatch[] }>(`api/attributes?q=${encodeURIComponent(q)}&limit=40`),
  profiles: () => request<ProfileList>("api/profiles"),
  loadProfile: (name: string) =>
    request<{ config: Record<string, unknown> }>(`api/profiles/${encodeURIComponent(name)}`),
  saveProfile: (name: string, cfg: unknown) =>
    request<unknown>(`api/profiles/${encodeURIComponent(name)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    }),
  deleteProfile: (name: string) =>
    request<unknown>(`api/profiles/${encodeURIComponent(name)}`, { method: "DELETE" }),
  start: (cfg: unknown) =>
    request<Status>("api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    }),
  stop: () => request<Status>("api/run", { method: "DELETE" }),
};

export const emptyStatus: Status = {
  state: "idle",
  config: {},
  elapsedMs: 0,
  exchanges: null,
  dynAuth: {
    disconnectRequests: 0,
    disconnectAcks: 0,
    disconnectNaks: 0,
    coaRequests: 0,
    coaAcks: 0,
    coaNaks: 0,
    unverified: 0,
    malformed: 0,
  },
  sessionsStarted: 0,
  sessionsCompleted: 0,
  sessionsOnline: 0,
  outstanding: 0,
  setupRate: 0,
  answerRate: 0,
  lossRate: 0,
  offeredRate: 0,
  achievedRate: 0,
  links: 0,
};
