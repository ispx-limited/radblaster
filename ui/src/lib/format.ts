// Durations arrive in nanoseconds. A RADIUS exchange spans microseconds to
// seconds, so the unit is chosen per value rather than fixed.
export function duration(ns: number): string {
  if (!ns) return "-";
  if (ns < 1_000) return `${ns}ns`;
  if (ns < 1_000_000) return `${(ns / 1_000).toFixed(ns < 10_000 ? 1 : 0)}µs`;
  if (ns < 1_000_000_000) return `${(ns / 1_000_000).toFixed(ns < 10_000_000 ? 2 : 1)}ms`;
  return `${(ns / 1_000_000_000).toFixed(2)}s`;
}

// Counts stay exact below ten thousand, where the precise figure is more
// useful than a rounded one.
export function count(n: number): string {
  if (n === undefined || n === null) return "0";
  if (n < 10_000) return n.toLocaleString();
  if (n < 1_000_000) return `${(n / 1_000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(2)}M`;
}

export function rate(n: number): string {
  return `${count(Math.round(n || 0))}/s`;
}

export function elapsed(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}
