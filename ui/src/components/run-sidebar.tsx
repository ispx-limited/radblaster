import { Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Field, Row } from "@/components/field";
import type { RunConfig, State } from "@/lib/api";

// The sidebar is what changes between runs: where to aim, how hard, and go.
// Everything describing the subscribers themselves lives on the profile tab,
// because that is edited once and reused.
export function RunSidebar({
  cfg,
  set,
  state,
  error,
  onStart,
  onStop,
}: {
  cfg: RunConfig;
  set: (patch: Partial<RunConfig>) => void;
  state: State;
  error?: string;
  onStart: () => void;
  onStop: () => void;
}) {
  const running = state === "running";

  return (
    <Card className="gap-0">
      <CardHeader className="pb-4">
        <CardTitle className="text-sm font-semibold">Target</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Field id="server" label="Authentication server">
          <Input
            id="server"
            placeholder="10.0.0.1:1812"
            value={cfg.server}
            disabled={running}
            onChange={(e) => set({ server: e.target.value })}
          />
        </Field>
        <Field id="acctServer" label="Accounting server" hint="Leave empty to skip accounting.">
          <Input
            id="acctServer"
            placeholder="10.0.0.1:1813"
            value={cfg.acctServer}
            disabled={running}
            onChange={(e) => set({ acctServer: e.target.value })}
          />
        </Field>
        <Field
          id="secret"
          label="Shared secret"
          hint="Never saved to a profile and never sent back to the browser."
        >
          <Input
            id="secret"
            type="password"
            value={cfg.secret}
            disabled={running}
            onChange={(e) => set({ secret: e.target.value })}
          />
        </Field>

        <Row>
          <Field id="rate" label="Sessions per second">
            <Input
              id="rate"
              type="number"
              min="0"
              step="any"
              placeholder="0 for max"
              value={cfg.rate}
              disabled={running}
              onChange={(e) => set({ rate: e.target.value })}
            />
          </Field>
          <Field id="count" label="Session count">
            <Input
              id="count"
              type="number"
              min="0"
              placeholder="0"
              value={cfg.count}
              disabled={running}
              onChange={(e) => set({ count: e.target.value })}
            />
          </Field>
        </Row>
        <Row>
          <Field id="duration" label="Duration">
            <Input
              id="duration"
              placeholder="30s"
              value={cfg.duration}
              disabled={running}
              onChange={(e) => set({ duration: e.target.value })}
            />
          </Field>
          <Field id="shape" label="Load shape">
            <Select value={cfg.shape} onValueChange={(v) => set({ shape: v })} disabled={running}>
              <SelectTrigger id="shape" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="steady">Steady</SelectItem>
                <SelectItem value="ramp">Ramp</SelectItem>
                <SelectItem value="burst">Burst</SelectItem>
                <SelectItem value="stampede">Stampede</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </Row>
        <p className="text-[11px] leading-relaxed text-muted-foreground/70">
          {cfg.shape === "ramp"
            ? "Climbs from nothing to the rate across the run, so the point where latency turns up is the capacity."
            : cfg.shape === "burst"
              ? "Alternates between the rate and the quiet fraction, which catches a server that copes with load but not with change."
              : cfg.shape === "stampede"
                ? "Offers everything at once, the shape of a reload where every subscriber returns together."
                : "A run needs either a count or a duration."}
        </p>

        {cfg.shape === "burst" ? (
          <Row>
            <Field id="burstPeriod" label="Burst period">
              <Input
                id="burstPeriod"
                placeholder="10s"
                value={cfg.burstPeriod}
                disabled={running}
                onChange={(e) => set({ burstPeriod: e.target.value })}
              />
            </Field>
            <Field id="burstLow" label="Quiet fraction">
              <Input
                id="burstLow"
                type="number"
                min="0"
                max="1"
                step="0.05"
                placeholder="0"
                value={cfg.burstLow}
                disabled={running}
                onChange={(e) => set({ burstLow: e.target.value })}
              />
            </Field>
          </Row>
        ) : null}

        {running ? (
          <Button type="button" variant="outline" className="w-full" onClick={onStop}>
            <Square className="size-3.5" /> Stop run
          </Button>
        ) : (
          <Button type="button" className="w-full" onClick={onStart}>
            <Play className="size-3.5" /> Start run
          </Button>
        )}

        {error ? (
          <p className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {error}
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
