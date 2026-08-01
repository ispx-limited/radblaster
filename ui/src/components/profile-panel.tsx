import { useEffect, useState } from "react";
import { Download, FolderOpen, Save, Trash2, Upload } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Field, Row } from "@/components/field";
import { AttrEditor } from "@/components/attr-editor";
import { api, ApiError, fromWire, toWire, type RunConfig, type State } from "@/lib/api";

export function ProfilePanel({
  cfg,
  set,
  replace,
  state,
}: {
  cfg: RunConfig;
  set: (patch: Partial<RunConfig>) => void;
  replace: (next: RunConfig) => void;
  state: State;
}) {
  const running = state === "running";
  const ipoe = cfg.auth === "ipoe";

  const [names, setNames] = useState<string[]>([]);
  const [dir, setDir] = useState("");
  const [selected, setSelected] = useState("");
  const [name, setName] = useState("");
  const [msg, setMsg] = useState<{ text: string; bad?: boolean }>();

  const refresh = () =>
    api
      .profiles()
      .then((r) => {
        setNames(r.profiles);
        setDir(r.dir);
      })
      .catch(() => setNames([]));

  useEffect(() => {
    void refresh();
  }, []);

  async function save() {
    if (!name.trim()) {
      setMsg({ text: "Give the profile a name first.", bad: true });
      return;
    }
    try {
      await api.saveProfile(name.trim(), toWire(cfg));
      setMsg({ text: `Saved as ${name.trim()}.` });
      setSelected(name.trim());
      await refresh();
    } catch (e) {
      setMsg({ text: e instanceof ApiError ? e.message : "could not save", bad: true });
    }
  }

  async function load(n: string) {
    try {
      const r = await api.loadProfile(n);
      // The secret is kept: a profile never carries one, and losing what is
      // already typed would mean retyping it on every load.
      const next = fromWire(r.config);
      replace({ ...next, secret: cfg.secret, password: cfg.password });
      setSelected(n);
      setName(n);
      setMsg({ text: `Loaded ${n}. Credentials are not part of a profile.` });
    } catch (e) {
      setMsg({ text: e instanceof ApiError ? e.message : "could not load", bad: true });
    }
  }

  async function remove() {
    if (!selected) return;
    try {
      await api.deleteProfile(selected);
      setSelected("");
      setMsg({ text: `Deleted ${selected}.` });
      await refresh();
    } catch (e) {
      setMsg({ text: e instanceof ApiError ? e.message : "could not delete", bad: true });
    }
  }

  // Export and import go through the browser so a profile can be moved between
  // machines without either end needing access to the other's disk.
  function exportFile() {
    const body = JSON.stringify(toWire(cfg), null, 2);
    const url = URL.createObjectURL(new Blob([body], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${name.trim() || "profile"}.json`;
    a.click();
    URL.revokeObjectURL(url);
  }

  function importFile(file: File) {
    file
      .text()
      .then((text) => {
        const next = fromWire(JSON.parse(text));
        replace({ ...next, secret: cfg.secret, password: cfg.password });
        setMsg({ text: `Loaded ${file.name}.` });
      })
      .catch(() => setMsg({ text: "That file is not a profile.", bad: true }));
  }

  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <div className="space-y-4">
        <Card className="gap-0">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">Subscribers</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Field id="auth" label="Access method">
              <Select value={cfg.auth} onValueChange={(v) => set({ auth: v })} disabled={running}>
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
            <Field
              id="username"
              label="Username template"
              hint={
                <>
                  <span className="font-mono">{"{n}"}</span> becomes the session number, so every
                  subscriber is distinct.
                </>
              }
            >
              <Input
                id="username"
                className="font-mono text-xs"
                placeholder="subscriber{n}@example.net"
                value={cfg.username}
                disabled={running}
                onChange={(e) => set({ username: e.target.value })}
              />
            </Field>
            <Field
              id="password"
              label={ipoe ? "Service password" : "Password"}
              hint={
                ipoe
                  ? "IPoE subscribers do not authenticate. This is the fixed string the access node sends so the request carries one, the same for every subscriber."
                  : "Never saved to a profile."
              }
            >
              <Input
                id="password"
                type="password"
                value={cfg.password}
                disabled={running}
                onChange={(e) => set({ password: e.target.value })}
              />
            </Field>
            <Field
              id="rejectRate"
              label="Unknown usernames"
              hint="A share of sessions, from 0 to 1, given a name no server knows. Rejection is often a slower path than acceptance."
            >
              <Input
                id="rejectRate"
                type="number"
                min="0"
                max="1"
                step="0.01"
                placeholder="0"
                value={cfg.rejectRate}
                disabled={running}
                onChange={(e) => set({ rejectRate: e.target.value })}
              />
            </Field>
          </CardContent>
        </Card>

        <Card className="gap-0">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">Access node</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Row>
              <Field id="nasIdentifier" label="NAS-Identifier">
                <Input
                  id="nasIdentifier"
                  placeholder="bng-01"
                  value={cfg.nasIdentifier}
                  disabled={running}
                  onChange={(e) => set({ nasIdentifier: e.target.value })}
                />
              </Field>
              <Field id="nasIPAddress" label="NAS-IP-Address">
                <Input
                  id="nasIPAddress"
                  placeholder="192.0.2.1"
                  value={cfg.nasIPAddress}
                  disabled={running}
                  onChange={(e) => set({ nasIPAddress: e.target.value })}
                />
              </Field>
            </Row>
            <Row>
              <Field id="nasPortType" label="NAS-Port-Type" hint="15 is Ethernet, 16 xDSL.">
                <Input
                  id="nasPortType"
                  type="number"
                  min="0"
                  placeholder="15"
                  value={cfg.nasPortType}
                  disabled={running}
                  onChange={(e) => set({ nasPortType: e.target.value })}
                />
              </Field>
              <Field id="coaListen" label="Accept CoA on">
                <Input
                  id="coaListen"
                  placeholder=":3799"
                  value={cfg.coaListen}
                  disabled={running}
                  onChange={(e) => set({ coaListen: e.target.value })}
                />
              </Field>
            </Row>
            <Field id="nasPortId" label="NAS-Port-Id">
              <Input
                id="nasPortId"
                className="font-mono text-xs"
                placeholder="eth 1/1/{n}"
                value={cfg.nasPortId}
                disabled={running}
                onChange={(e) => set({ nasPortId: e.target.value })}
              />
            </Field>
            <Field id="callingStationId" label="Calling-Station-Id">
              <Input
                id="callingStationId"
                className="font-mono text-xs"
                placeholder="00:1A:2B:00:00:{n}"
                value={cfg.callingStationId}
                disabled={running}
                onChange={(e) => set({ callingStationId: e.target.value })}
              />
            </Field>
          </CardContent>
        </Card>
      </div>

      <div className="space-y-4">
        <Card className="gap-0">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">Extra attributes</CardTitle>
          </CardHeader>
          <CardContent>
            <AttrEditor
              value={cfg.attributes}
              onChange={(attributes) => set({ attributes })}
              disabled={running}
            />
          </CardContent>
        </Card>

        <Card className="gap-0">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">Session and traffic</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Row>
              <Field id="hold" label="Hold online">
                <Input
                  id="hold"
                  placeholder="30m"
                  value={cfg.hold}
                  disabled={running}
                  onChange={(e) => set({ hold: e.target.value })}
                />
              </Field>
              <Field id="interim" label="Interim interval">
                <Input
                  id="interim"
                  placeholder="300s"
                  value={cfg.interim}
                  disabled={running}
                  onChange={(e) => set({ interim: e.target.value })}
                />
              </Field>
            </Row>
            <Row>
              <Field id="trafficUp" label="Up, min octets/s">
                <Input
                  id="trafficUp"
                  type="number"
                  min="0"
                  value={cfg.trafficUp}
                  disabled={running}
                  onChange={(e) => set({ trafficUp: e.target.value })}
                />
              </Field>
              <Field id="trafficUpMax" label="Up, max">
                <Input
                  id="trafficUpMax"
                  type="number"
                  min="0"
                  placeholder="same as min"
                  value={cfg.trafficUpMax}
                  disabled={running}
                  onChange={(e) => set({ trafficUpMax: e.target.value })}
                />
              </Field>
            </Row>
            <Row>
              <Field id="trafficDown" label="Down, min octets/s">
                <Input
                  id="trafficDown"
                  type="number"
                  min="0"
                  value={cfg.trafficDown}
                  disabled={running}
                  onChange={(e) => set({ trafficDown: e.target.value })}
                />
              </Field>
              <Field id="trafficDownMax" label="Down, max">
                <Input
                  id="trafficDownMax"
                  type="number"
                  min="0"
                  placeholder="same as min"
                  value={cfg.trafficDownMax}
                  disabled={running}
                  onChange={(e) => set({ trafficDownMax: e.target.value })}
                />
              </Field>
            </Row>
            <p className="text-[11px] leading-relaxed text-muted-foreground/70">
              Set a maximum above the minimum and every subscriber draws its own rate from that
              range, so the estate looks like a spread rather than one subscriber repeated.
            </p>
            <Separator />
            <Row>
              <Field id="links" label="Links per endpoint" hint="256 requests each.">
                <Input
                  id="links"
                  type="number"
                  min="0"
                  placeholder="auto"
                  value={cfg.links}
                  disabled={running}
                  onChange={(e) => set({ links: e.target.value })}
                />
              </Field>
              <Field id="timeout" label="Reply timeout">
                <Input
                  id="timeout"
                  placeholder="5s"
                  value={cfg.timeout}
                  disabled={running}
                  onChange={(e) => set({ timeout: e.target.value })}
                />
              </Field>
            </Row>
            <Row>
              <Field id="retries" label="Retries" hint="0 measures raw loss.">
                <Input
                  id="retries"
                  type="number"
                  min="0"
                  value={cfg.retries}
                  disabled={running}
                  onChange={(e) => set({ retries: e.target.value })}
                />
              </Field>
              <Field id="seed" label="Seed" hint="Set one to reproduce a run.">
                <Input
                  id="seed"
                  type="number"
                  min="0"
                  placeholder="fresh"
                  value={cfg.seed}
                  disabled={running}
                  onChange={(e) => set({ seed: e.target.value })}
                />
              </Field>
            </Row>
          </CardContent>
        </Card>

        <Card className="gap-0">
          <CardHeader className="pb-4">
            <CardTitle className="text-sm font-semibold">Profiles</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Row>
              <Field id="profileName" label="Name">
                <Input
                  id="profileName"
                  placeholder="nokia-business"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </Field>
              <Field id="profileOpen" label="Saved">
                <Select value={selected} onValueChange={load} disabled={names.length === 0}>
                  <SelectTrigger id="profileOpen" className="w-full">
                    <SelectValue placeholder={names.length ? "Open" : "None saved"} />
                  </SelectTrigger>
                  <SelectContent>
                    {names.map((n) => (
                      <SelectItem key={n} value={n}>
                        {n}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </Row>

            <div className="flex flex-wrap gap-2">
              <Button type="button" size="sm" onClick={save}>
                <Save className="size-3.5" /> Save
              </Button>
              <Button type="button" size="sm" variant="outline" onClick={exportFile}>
                <Download className="size-3.5" /> Export
              </Button>
              <Button type="button" size="sm" variant="outline" asChild>
                <label className="cursor-pointer">
                  <Upload className="size-3.5" /> Import
                  <input
                    type="file"
                    accept="application/json,.json"
                    className="hidden"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) importFile(f);
                      e.target.value = "";
                    }}
                  />
                </label>
              </Button>
              <Button type="button" size="sm" variant="ghost" onClick={remove} disabled={!selected}>
                <Trash2 className="size-3.5" /> Delete
              </Button>
            </div>

            {msg ? (
              <p className={msg.bad ? "text-xs text-destructive" : "text-xs text-muted-foreground"}>
                {msg.text}
              </p>
            ) : null}

            {dir ? (
              <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground/70">
                <FolderOpen className="size-3" />
                Kept as YAML in <span className="font-mono">{dir}</span>, so the same file can be
                run from the command line and committed alongside whatever else describes the estate.
              </p>
            ) : null}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
