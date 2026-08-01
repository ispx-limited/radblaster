import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import type { Attr } from "@/lib/api";

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
  const set = (i: number, patch: Partial<Attr>) =>
    onChange(value.map((a, n) => (n === i ? { ...a, ...patch } : a)));

  return (
    <div className="space-y-2">
      {value.length === 0 ? (
        <p className="text-[11px] text-muted-foreground/70">
          None. Vendor attributes go here, by dictionary name or by number.
        </p>
      ) : null}

      {value.map((a, i) => (
        <div key={i} className="flex gap-2">
          <Input
            aria-label={`Attribute ${i + 1} name`}
            className="flex-[2] font-mono text-xs"
            placeholder="Cisco-AVPair"
            value={a.name}
            disabled={disabled}
            onChange={(e) => set(i, { name: e.target.value })}
          />
          <Input
            aria-label={`Attribute ${i + 1} value`}
            className="flex-[3] font-mono text-xs"
            placeholder="ip:qos-policy-in=voice"
            value={a.value}
            disabled={disabled}
            onChange={(e) => set(i, { value: e.target.value })}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label={`Remove attribute ${i + 1}`}
            disabled={disabled}
            onClick={() => onChange(value.filter((_, n) => n !== i))}
          >
            <X className="size-3.5" />
          </Button>
        </div>
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

      <p className="text-[11px] text-muted-foreground/70">
        A name resolves through the loaded dictionary. Without one, write it by number
        as <span className="font-mono">26.9.1</span>. Values take <span className="font-mono">{"{n}"}</span>,
        the session number.
      </p>
    </div>
  );
}
