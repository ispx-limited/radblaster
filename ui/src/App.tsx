import { useEffect, useState } from "react";
import { Radio } from "lucide-react";
import { api, ApiError, emptyStatus, type Status } from "@/lib/api";
import { elapsed } from "@/lib/format";
import { cn } from "@/lib/utils";
import { RunForm } from "@/components/run-form";
import { StatCards } from "@/components/stat-cards";
import { ExchangeTable, DynAuthTable } from "@/components/exchange-table";
import { RateChart, LatencyChart } from "@/components/charts";
import { Notes } from "@/components/notes";

const STATE_LABEL: Record<Status["state"], string> = {
  idle: "Idle",
  running: "Running",
  done: "Finished",
  failed: "Failed",
};

function StateBadge({ state }: { state: Status["state"] }) {
  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-border px-3 py-1 text-xs text-muted-foreground">
      <Radio
        className={cn(
          "size-3",
          state === "running" && "animate-pulse text-signal",
          state === "done" && "text-info",
          state === "failed" && "text-destructive",
          state === "idle" && "text-muted-foreground/50"
        )}
      />
      {STATE_LABEL[state]}
    </span>
  );
}

export default function App() {
  const [status, setStatus] = useState<Status>(emptyStatus);
  const [error, setError] = useState<string>();

  // The series comes from the server rather than being accumulated here, so a
  // reload or a browser joining partway through still sees the whole run.
  const history = status.history ?? [];

  // The stream carries the whole picture on every message, so arriving late or
  // reconnecting after a drop still shows the full state.
  useEffect(() => {
    let es: EventSource | null = null;
    let retry: ReturnType<typeof setTimeout>;
    let closed = false;

    const connect = () => {
      es = new EventSource("api/events");
      es.onmessage = (ev) => {
        setStatus(JSON.parse(ev.data) as Status);
      };
      es.onerror = () => {
        es?.close();
        if (!closed) retry = setTimeout(connect, 1500);
      };
    };

    api.status().then(setStatus).catch(() => {});
    connect();
    return () => {
      closed = true;
      clearTimeout(retry);
      es?.close();
    };
  }, []);

  async function start(cfg: Record<string, unknown>) {
    setError(undefined);
    try {
      setStatus(await api.start(cfg));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "could not start the run");
    }
  }

  async function stop() {
    try {
      await api.stop();
    } catch {
      // The stream reports the outcome either way.
    }
  }

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-10 flex items-center gap-3 border-b border-border bg-card/80 px-5 py-3 backdrop-blur">
        <div className="size-5 rounded-md bg-gradient-to-br from-[#e53935] to-[#9b1c1c]" />
        <h1 className="text-sm font-bold tracking-tight">radblaster</h1>
        <span className="text-xs text-muted-foreground/70">RADIUS load testing</span>
        <div className="flex-1" />
        {status.state !== "idle" ? (
          <span className="text-xs tabular-nums text-muted-foreground">{elapsed(status.elapsedMs)}</span>
        ) : null}
        <StateBadge state={status.state} />
      </header>

      <main className="grid items-start gap-4 p-5 lg:grid-cols-[340px_1fr]">
        <RunForm state={status.state} error={error ?? status.error} onStart={start} onStop={stop} />

        <div className="space-y-4">
          <StatCards status={status} />
          <div className="grid gap-4 xl:grid-cols-2">
            <RateChart data={history} />
            <LatencyChart data={history} />
          </div>
          <ExchangeTable status={status} />
          <DynAuthTable status={status} />
          <Notes notes={status.notes} />
        </div>
      </main>
    </div>
  );
}
