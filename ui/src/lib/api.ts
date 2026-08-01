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

export const api = {
  status: () => request<Status>("api/status"),
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
