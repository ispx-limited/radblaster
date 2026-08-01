import { useState, type FormEvent } from "react";
import { Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { State } from "@/lib/api";

const NUMERIC = new Set([
  "rate", "count", "trafficUp", "trafficUpMax", "trafficDown", "trafficDownMax",
  "links", "retries", "seed", "nasPortType", "rejectRate", "burstLow",
]);

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="text-xs text-muted-foreground">
        {label}
      </Label>
      {children}
      {hint ? <p className="text-[11px] text-muted-foreground/70">{hint}</p> : null}
    </div>
  );
}

export function RunForm({
  state,
  error,
  onStart,
  onStop,
}: {
  state: State;
  error?: string;
  onStart: (cfg: Record<string, unknown>) => void;
  onStop: () => void;
}) {
  const running = state === "running";
  const [auth, setAuth] = useState("pap");
  const [shape, setShape] = useState("steady");
  const ipoe = auth === "ipoe";

  function submit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    const data = new FormData(ev.currentTarget);
    const cfg: Record<string, unknown> = { auth, shape, retries: 2 };
    for (const [k, v] of data.entries()) {
      const s = String(v).trim();
      if (!s) continue;
      if (k === "attributes") {
        cfg[k] = s.split("\n").map((x) => x.trim()).filter(Boolean);
        continue;
      }
      cfg[k] = NUMERIC.has(k) ? Number(s) : s;
    }
    onStart(cfg);
  }

  return (
    <Card className="gap-0">
      <CardHeader className="pb-4">
        <CardTitle className="text-sm font-semibold">Run</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} autoComplete="off" className="space-y-3">
          <Field id="server" label="Authentication server">
            <Input id="server" name="server" placeholder="10.0.0.1:1812" required disabled={running} />
          </Field>
          <Field id="acctServer" label="Accounting server" hint="Leave empty to skip accounting.">
            <Input id="acctServer" name="acctServer" placeholder="10.0.0.1:1813" disabled={running} />
          </Field>
          <Field id="secret" label="Shared secret">
            <Input id="secret" name="secret" type="password" required disabled={running} />
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field id="rate" label="Sessions per second">
              <Input id="rate" name="rate" type="number" min="0" step="any" placeholder="0 for max" disabled={running} />
            </Field>
            <Field id="count" label="Session count">
              <Input id="count" name="count" type="number" min="0" placeholder="0" disabled={running} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field id="duration" label="Duration">
              <Input id="duration" name="duration" placeholder="30s" disabled={running} />
            </Field>
            <Field id="hold" label="Hold online">
              <Input id="hold" name="hold" placeholder="0s" disabled={running} />
            </Field>
          </div>
          <p className="text-[11px] text-muted-foreground/70">A run needs either a count or a duration.</p>

          <div className="grid grid-cols-2 gap-3">
            <Field id="shape" label="Load shape">
              <Select value={shape} onValueChange={setShape} disabled={running}>
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
            <Field id="rejectRate" label="Unknown usernames">
              <Input
                id="rejectRate"
                name="rejectRate"
                type="number"
                min="0"
                max="1"
                step="0.01"
                placeholder="0"
                disabled={running}
              />
            </Field>
          </div>
          <p className="text-[11px] text-muted-foreground/70">
            {shape === "ramp"
              ? "Climbs from nothing to the rate across the run, so the point where latency turns up is the capacity."
              : shape === "burst"
                ? "Alternates between the rate and the quiet fraction, which catches a server that copes with load but not with change."
                : shape === "stampede"
                  ? "Offers everything at once, the shape of a reload where every subscriber returns together."
                  : "Holds the rate flat."}
            {" "}A fraction of unknown usernames exercises the rejection path, which is often slower than acceptance.
          </p>

          {shape === "burst" ? (
            <div className="grid grid-cols-2 gap-3">
              <Field id="burstPeriod" label="Burst period">
                <Input id="burstPeriod" name="burstPeriod" placeholder="10s" disabled={running} />
              </Field>
              <Field id="burstLow" label="Quiet fraction">
                <Input id="burstLow" name="burstLow" type="number" min="0" max="1" step="0.05" placeholder="0" disabled={running} />
              </Field>
            </div>
          ) : null}

          <Accordion type="multiple" className="border-t border-border/60">
            <AccordionItem value="subscribers">
              <AccordionTrigger className="text-xs text-muted-foreground hover:no-underline">
                Subscribers and attributes
              </AccordionTrigger>
              <AccordionContent className="space-y-3 pt-1">
                <Field id="username" label="Username template" hint="{n} becomes the session number.">
                  <Input id="username" name="username" placeholder="subscriber{n}@example.net" disabled={running} />
                </Field>
                <Field
                  id="password"
                  label={ipoe ? "Service password" : "Password"}
                  hint={
                    ipoe
                      ? "IPoE subscribers do not authenticate. This is the fixed string the access node sends so the request carries one, the same for every subscriber."
                      : undefined
                  }
                >
                  <Input id="password" name="password" type="password" disabled={running} />
                </Field>
                <div className="grid grid-cols-2 gap-3">
                  <Field id="auth" label="Access method">
                    <Select value={auth} onValueChange={setAuth} disabled={running}>
                      <SelectTrigger id="auth" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="pap">PPPoE, PAP</SelectItem>
                        <SelectItem value="chap">PPPoE, CHAP</SelectItem>
                        <SelectItem value="ipoe">IPoE</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field id="nasIdentifier" label="NAS-Identifier">
                    <Input id="nasIdentifier" name="nasIdentifier" placeholder="bng-01" disabled={running} />
                  </Field>
                </div>
                <Field id="attributes" label="Extra attributes, one per line">
                  <Textarea
                    id="attributes"
                    name="attributes"
                    rows={3}
                    className="font-mono text-xs"
                    placeholder={"Cisco-AVPair=ip:qos-policy-in=voice\n26.3561.1=eth 1/1/{n}"}
                    disabled={running}
                  />
                </Field>
              </AccordionContent>
            </AccordionItem>

            <AccordionItem value="timing">
              <AccordionTrigger className="text-xs text-muted-foreground hover:no-underline">
                Accounting and timing
              </AccordionTrigger>
              <AccordionContent className="space-y-3 pt-1">
                <div className="grid grid-cols-2 gap-3">
                  <Field id="interim" label="Interim interval">
                    <Input id="interim" name="interim" placeholder="300s" disabled={running} />
                  </Field>
                  <Field id="timeout" label="Reply timeout">
                    <Input id="timeout" name="timeout" placeholder="5s" disabled={running} />
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Field id="trafficUp" label="Traffic up, min octets/s">
                    <Input id="trafficUp" name="trafficUp" type="number" min="0" disabled={running} />
                  </Field>
                  <Field id="trafficUpMax" label="Traffic up, max">
                    <Input id="trafficUpMax" name="trafficUpMax" type="number" min="0" placeholder="same as min" disabled={running} />
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Field id="trafficDown" label="Traffic down, min octets/s">
                    <Input id="trafficDown" name="trafficDown" type="number" min="0" disabled={running} />
                  </Field>
                  <Field id="trafficDownMax" label="Traffic down, max">
                    <Input id="trafficDownMax" name="trafficDownMax" type="number" min="0" placeholder="same as min" disabled={running} />
                  </Field>
                </div>
                <p className="text-[11px] text-muted-foreground/70">
                  Set a maximum above the minimum and every subscriber draws its own rate from
                  that range, so the estate looks like a spread rather than one subscriber repeated.
                </p>
                <div className="grid grid-cols-2 gap-3">
                  <Field id="links" label="Links per endpoint">
                    <Input id="links" name="links" type="number" min="0" placeholder="auto" disabled={running} />
                  </Field>
                  <Field id="retries" label="Retries">
                    <Input id="retries" name="retries" type="number" min="0" defaultValue={2} disabled={running} />
                  </Field>
                </div>
                <Field id="coaListen" label="Accept CoA and Disconnect on">
                  <Input id="coaListen" name="coaListen" placeholder=":3799" disabled={running} />
                </Field>
                <Field id="seed" label="Seed" hint="Set one to make a run reproducible.">
                  <Input id="seed" name="seed" type="number" min="0" placeholder="0 for a fresh one" disabled={running} />
                </Field>
              </AccordionContent>
            </AccordionItem>
          </Accordion>

          {running ? (
            <Button type="button" variant="outline" className="w-full" onClick={onStop}>
              <Square className="size-3.5" /> Stop run
            </Button>
          ) : (
            <Button type="submit" className="w-full">
              <Play className="size-3.5" /> Start run
            </Button>
          )}

          {error ? (
            <p className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {error}
            </p>
          ) : null}
        </form>
      </CardContent>
    </Card>
  );
}
