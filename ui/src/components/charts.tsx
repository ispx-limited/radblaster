import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart";
import { count, duration } from "@/lib/format";

import type { Sample } from "@/lib/api";

const rateConfig = {
  offered: { label: "Offered", color: "var(--chart-1)" },
  answered: { label: "Answered", color: "var(--color-signal)" },
  lost: { label: "Lost", color: "var(--chart-3)" },
} satisfies ChartConfig;

const latencyConfig = {
  p95: { label: "From scheduled send", color: "var(--chart-2)" },
  service: { label: "From actual send", color: "var(--chart-5)" },
} satisfies ChartConfig;

function seconds(ms: number) {
  return `${Math.round(ms / 1000)}s`;
}

function Empty() {
  return (
    <div className="flex h-[190px] items-center justify-center text-sm text-muted-foreground">
      No data yet.
    </div>
  );
}

// Animation is off and dots are hidden deliberately: the series updates twice a
// second, and an animated redraw at that cadence never settles.
const area = { isAnimationActive: false, dot: false, strokeWidth: 2, fillOpacity: 0.12 } as const;

export function RateChart({ data }: { data: Sample[] }) {
  return (
    <Card className="gap-0">
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-semibold">Rates</CardTitle>
      </CardHeader>
      <CardContent>
        {data.length < 2 ? <Empty /> : (
        <ChartContainer config={rateConfig} className="h-[190px] w-full">
          <AreaChart data={data} margin={{ top: 6, left: 4, right: 8, bottom: 0 }}>
            <CartesianGrid vertical={false} />
            <XAxis dataKey="t" tickLine={false} axisLine={false} tickMargin={8} minTickGap={44} tickFormatter={seconds} />
            <YAxis tickLine={false} axisLine={false} width={44} tickFormatter={(v: number) => count(v)} />
            <ChartTooltip content={<ChartTooltipContent labelFormatter={(v) => seconds(Number(v))} />} />
            <Area type="monotone" dataKey="offered" stroke="var(--color-offered)" fill="var(--color-offered)" {...area} />
            <Area type="monotone" dataKey="answered" stroke="var(--color-answered)" fill="var(--color-answered)" {...area} />
            <Area type="monotone" dataKey="lost" stroke="var(--color-lost)" fill="var(--color-lost)" {...area} />
          </AreaChart>
        </ChartContainer>
        )}
      </CardContent>
    </Card>
  );
}

export function LatencyChart({ data }: { data: Sample[] }) {
  return (
    <Card className="gap-0">
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-semibold">Authentication latency, 95th percentile</CardTitle>
      </CardHeader>
      <CardContent>
        {data.length < 2 ? <Empty /> : (
        <ChartContainer config={latencyConfig} className="h-[190px] w-full">
          <AreaChart data={data} margin={{ top: 6, left: 4, right: 8, bottom: 0 }}>
            <CartesianGrid vertical={false} />
            <XAxis dataKey="t" tickLine={false} axisLine={false} tickMargin={8} minTickGap={44} tickFormatter={seconds} />
            <YAxis tickLine={false} axisLine={false} width={54} tickFormatter={(v: number) => duration(v)} />
            <ChartTooltip
              content={<ChartTooltipContent labelFormatter={(v) => seconds(Number(v))} formatter={(v) => duration(Number(v))} />}
            />
            <Area type="monotone" dataKey="p95" stroke="var(--color-p95)" fill="var(--color-p95)" {...area} />
            <Area type="monotone" dataKey="service" stroke="var(--color-service)" fill="var(--color-service)" {...area} />
          </AreaChart>
        </ChartContainer>
        )}
      </CardContent>
    </Card>
  );
}
