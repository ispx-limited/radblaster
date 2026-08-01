import { useEffect, useRef, useState } from "react";
import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, type Attr, type AttrMatch } from "@/lib/api";
import { cn } from "@/lib/utils";

// Data types that carry a fixed set of named constants worth offering.
const HAS_NAMED_VALUES = new Set([5, 7, 8]);

function Suggestions({
  items,
  onPick,
  render,
}: {
  items: string[];
  onPick: (v: string) => void;
  render?: (v: string) => React.ReactNode;
}) {
  if (items.length === 0) return null;
  return (
    <ul
      role="listbox"
      className="absolute z-20 mt-1 max-h-56 w-full overflow-auto rounded-md border border-border bg-popover p-1 shadow-md"
    >
      {items.map((v) => (
        <li key={v}>
          <button
            type="button"
            role="option"
            className="w-full rounded-sm px-2 py-1.5 text-left text-xs hover:bg-accent"
            // Mouse down rather than click: blur fires first and would close
            // the list before a click could land.
            onMouseDown={(e) => {
              e.preventDefault();
              onPick(v);
            }}
          >
            {render ? render(v) : v}
          </button>
        </li>
      ))}
    </ul>
  );
}

function AttrRow({
  attr,
  index,
  onChange,
  onRemove,
  disabled,
}: {
  attr: Attr;
  index: number;
  onChange: (patch: Partial<Attr>) => void;
  onRemove: () => void;
  disabled?: boolean;
}) {
  const [matches, setMatches] = useState<AttrMatch[]>([]);
  const [openNames, setOpenNames] = useState(false);
  const [openValues, setOpenValues] = useState(false);
  const [known, setKnown] = useState<AttrMatch>();
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);

  // Debounced so a name is looked up as it is typed without a request per
  // keystroke.
  useEffect(() => {
    if (!openNames) return;
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      api
        .attributes(attr.name)
        .then((r) => setMatches(r.matches))
        .catch(() => setMatches([]));
    }, 120);
    return () => clearTimeout(timer.current);
  }, [attr.name, openNames]);

  // Once a name resolves, remember what it is so its values can be offered.
  useEffect(() => {
    if (!attr.name.trim()) {
      setKnown(undefined);
      return;
    }
    let cancelled = false;
    api
      .attributes(attr.name)
      .then((r) => {
        if (cancelled) return;
        setKnown(r.matches.find((m) => m.name.toLowerCase() === attr.name.toLowerCase()));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [attr.name]);

  const valueChoices =
    known && known.values && known.values.length > 0 && HAS_NAMED_VALUES.has(known.data)
      ? known.values.filter((v) => v.toLowerCase().includes(attr.value.toLowerCase()))
      : [];

  return (
    <div className="flex items-start gap-2">
      <div className="relative flex-[2]">
        <Input
          aria-label={`Attribute ${index + 1} name`}
          className={cn("font-mono text-xs", attr.name && !known && "border-warn/60")}
          placeholder="Cisco-AVPair"
          value={attr.name}
          disabled={disabled}
          autoComplete="off"
          onFocus={() => setOpenNames(true)}
          onBlur={() => setOpenNames(false)}
          onChange={(e) => onChange({ name: e.target.value })}
        />
        {openNames ? (
          <Suggestions
            items={matches.map((m) => m.name)}
            onPick={(v) => {
              onChange({ name: v });
              setOpenNames(false);
            }}
            render={(v) => {
              const m = matches.find((x) => x.name === v);
              return (
                <span className="flex items-baseline justify-between gap-2">
                  <span className="font-mono">{v}</span>
                  {m?.vendor ? (
                    <span className="shrink-0 text-[10px] text-muted-foreground">
                      vendor {m.vendor}
                    </span>
                  ) : null}
                </span>
              );
            }}
          />
        ) : null}
      </div>

      <div className="relative flex-[3]">
        <Input
          aria-label={`Attribute ${index + 1} value`}
          className="font-mono text-xs"
          placeholder={valueChoices.length ? "pick or type a number" : "ip:qos-policy-in=voice"}
          value={attr.value}
          disabled={disabled}
          autoComplete="off"
          onFocus={() => setOpenValues(true)}
          onBlur={() => setOpenValues(false)}
          onChange={(e) => onChange({ value: e.target.value })}
        />
        {openValues && valueChoices.length > 0 ? (
          <Suggestions
            items={valueChoices}
            onPick={(v) => {
              onChange({ value: v });
              setOpenValues(false);
            }}
          />
        ) : null}
      </div>

      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label={`Remove attribute ${index + 1}`}
        disabled={disabled}
        onClick={onRemove}
      >
        <X className="size-3.5" />
      </Button>
    </div>
  );
}

// The attribute list is edited a row at a time rather than as free text. An
// attribute has two distinct halves, and a textarea makes the operator do the
// tool's parsing for it while hiding which line was wrong.
export function AttrEditor({
  value,
  onChange,
  disabled,
}: {
  value: Attr[];
  onChange: (next: Attr[]) => void;
  disabled?: boolean;
}) {
  return (
    <div className="space-y-2">
      {value.length === 0 ? (
        <p className="text-[11px] text-muted-foreground/70">
          None. Vendor attributes go here, by dictionary name or by number.
        </p>
      ) : null}

      {value.map((a, i) => (
        <AttrRow
          key={i}
          attr={a}
          index={i}
          disabled={disabled}
          onChange={(patch) => onChange(value.map((x, n) => (n === i ? { ...x, ...patch } : x)))}
          onRemove={() => onChange(value.filter((_, n) => n !== i))}
        />
      ))}

      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled}
        onClick={() => onChange([...value, { name: "", value: "" }])}
      >
        <Plus className="size-3.5" /> Add attribute
      </Button>

      <p className="text-[11px] leading-relaxed text-muted-foreground/70">
        Names come from the loaded dictionaries. An outlined field means the name is not
        one of them, which is fine if you meant a number such as{" "}
        <span className="font-mono">26.9.1</span>. Values take{" "}
        <span className="font-mono">{"{n}"}</span>, the session number.
      </p>
    </div>
  );
}
