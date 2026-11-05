import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { count, duration } from "@/lib/format";
import type { Status } from "@/lib/api";
import { cn } from "@/lib/utils";

const head = "h-9 bg-muted/40 text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground/70";
const num = "text-right tabular-nums";

function Num({ value, tone }: { value: number; tone?: "bad" | "warn" }) {
  return (
    <TableCell
      className={cn(num, value === 0 && "text-muted-foreground/50", value > 0 && tone === "bad" && "text-destructive", value > 0 && tone === "warn" && "text-warn")}
    >
      {count(value)}
    </TableCell>
  );
}

export function ExchangeTable({ status }: { status: Status }) {
  const rows = (status.exchanges || []).filter((e) => e.sent > 0);

  return (
    <Card className="gap-0">
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-semibold">Exchanges</CardTitle>
      </CardHeader>
      <CardContent className="px-0">
        {rows.length === 0 ? (
          <p className="px-6 py-8 text-center text-sm text-muted-foreground">No requests sent yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className={cn(head, "pl-6")}>Exchange</TableHead>
                <TableHead className={cn(head, num)}>Sent</TableHead>
                <TableHead className={cn(head, num)}>OK</TableHead>
                <TableHead className={cn(head, num)}>Rejected</TableHead>
                <TableHead className={cn(head, num)}>Retried</TableHead>
                <TableHead className={cn(head, num)}>Lost</TableHead>
                <TableHead className={cn(head, num)}>Unverified</TableHead>
                <TableHead className={cn(head, num)}>p50</TableHead>
                <TableHead className={cn(head, num)}>p95</TableHead>
                <TableHead className={cn(head, num, "pr-6")}>p99</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((e) => (
                <TableRow key={e.name}>
                  <TableCell className="pl-6 font-medium">{e.name}</TableCell>
                  <Num value={e.sent} />
                  <Num value={e.ok} />
                  <Num value={e.rejected} />
                  <Num value={e.retransmits} tone="warn" />
                  <Num value={e.timeouts} tone="bad" />
                  <Num value={e.unverified} tone="bad" />
                  <TableCell className={num}>{duration(e.p50)}</TableCell>
                  <TableCell className={num}>{duration(e.p95)}</TableCell>
                  <TableCell className={cn(num, "pr-6")}>{duration(e.p99)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

export function DynAuthTable({ status }: { status: Status }) {
  const d = status.dynAuth;
  const total = d.disconnectRequests + d.coaRequests + d.unverified + d.malformed;
  if (!total) return null;

  return (
    <Card className="gap-0">
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-semibold">Server-initiated</CardTitle>
      </CardHeader>
      <CardContent className="px-0">
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className={cn(head, "pl-6")}>Request</TableHead>
              <TableHead className={cn(head, num)}>Received</TableHead>
              <TableHead className={cn(head, num)}>Acknowledged</TableHead>
              <TableHead className={cn(head, num, "pr-6")}>Refused</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {d.disconnectRequests > 0 && (
              <TableRow>
                <TableCell className="pl-6 font-medium">Disconnect-Request</TableCell>
                <Num value={d.disconnectRequests} />
                <Num value={d.disconnectAcks} />
                <Num value={d.disconnectNaks} tone="warn" />
              </TableRow>
            )}
            {d.coaRequests > 0 && (
              <TableRow>
                <TableCell className="pl-6 font-medium">CoA-Request</TableCell>
                <Num value={d.coaRequests} />
                <Num value={d.coaAcks} />
                <Num value={d.coaNaks} tone="warn" />
              </TableRow>
            )}
            {d.unverified > 0 && (
              <TableRow>
                <TableCell className="pl-6 font-medium">Failed verification</TableCell>
                <Num value={d.unverified} tone="bad" />
                <TableCell className={num}>-</TableCell>
                <TableCell className={cn(num, "pr-6")}>-</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
