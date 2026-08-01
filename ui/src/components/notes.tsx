import { AlertTriangle } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function Notes({ notes }: { notes?: string[] }) {
  if (!notes || notes.length === 0) return null;

  return (
    <Card className="gap-0">
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-semibold">Notes</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {notes.map((n, i) => (
          <div key={i} className="flex gap-2.5 rounded-md border-l-2 border-warn bg-warn/8 px-3 py-2.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" />
            <p className="text-[13px] leading-relaxed text-muted-foreground">{n}</p>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
