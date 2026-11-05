import { Card, CardContent } from "@/components/ui/card";
import { count, duration, rate } from "@/lib/format";
import type { Status } from "@/lib/api";
import { cn } from "@/lib/utils";

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "alert" | "good" }) {
  return (
    <Card className="gap-0 py-4">
      <CardContent className="px-4">
        <dt className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground">{label}</dt>
        <dd
          className={cn(
            "mt-1.5 text-2xl font-semibold tabular-nums tracking-tight",
            tone === "alert" && "text-destructive",
            tone === "good" && "text-signal"
          )}
        >
          {value}
        </dd>
        <p className="mt-0.5 text-[11px] tabular-nums text-muted-foreground/70">{sub || " "}</p>
      </CardContent>
    </Card>
  );
}

export function StatCards({ status }: { status: Status }) {
  const auth = status.exchanges?.[0];
  let lost = 0;
  let retried = 0;
  for (const e of status.exchanges || []) {
    lost += e.timeouts;
    retried += e.retransmits;
  }

  return (
    <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      <Stat
        label="Sessions"
        value={count(auth?.sent ?? 0)}
        sub={status.state === "running" ? `${rate(status.setupRate)} now` : `${rate(status.achievedRate)} achieved`}
      />
      <Stat
        label="Online"
        value={count(status.sessionsOnline)}
        sub={status.sessionsCompleted ? `${count(status.sessionsCompleted)} completed` : undefined}
      />
      {/* Outstanding climbs before any latency figure moves, because a request
          has to time out before it can be counted slow. */}
      <Stat label="Awaiting reply" value={count(status.outstanding)} sub="in flight" />
      <Stat
        label="p95 latency"
        value={auth ? duration(auth.p95) : "-"}
        sub={auth ? `p99 ${duration(auth.p99)}` : undefined}
      />
      <Stat
        label="Lost"
        value={count(lost)}
        sub={retried ? `${count(retried)} retried` : undefined}
        tone={lost ? "alert" : "good"}
      />
    </dl>
  );
}
