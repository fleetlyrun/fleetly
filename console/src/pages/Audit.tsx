import { useState } from "react";
import { useAudit } from "../lib/catalog";
import { EmptyNote, ErrorNote, Field, LoadingNote, PageShell, Select, TableHead, TableWrap, TextInput, formatTime, shortId } from "../components/ui";

// 审计页（F3.1）：GET /v1/audit 只读面 + CLI 同款过滤器（source/action
// 前缀/actor/resource）。审计行服务端只写——Console 是观察面。
const SOURCES = ["", "api", "cli", "manual", "webhook", "schedule", "system"];

export function AuditPage() {
  const [source, setSource] = useState("");
  const [action, setAction] = useState("");
  const [actor, setActor] = useState("");
  const [resource, setResource] = useState("");
  const [applied, setApplied] = useState({ source: "", action: "", actor: "", resource: "" });
  const audit = useAudit({ ...applied, limit: 100 });

  return (
    <PageShell title="Audit" hint="server-written audit trail (GET /v1/audit)">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          setApplied({ source, action, actor, resource });
        }}
      >
        <Field label="Source">
          <Select value={source} onChange={(event) => setSource(event.target.value)}>
            {SOURCES.map((item) => (
              <option key={item} value={item}>
                {item === "" ? "any" : item}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Action prefix">
          <TextInput value={action} onChange={(event) => setAction(event.target.value)} placeholder="deployment." />
        </Field>
        <Field label="Actor">
          <TextInput value={actor} onChange={(event) => setActor(event.target.value)} />
        </Field>
        <Field label="Resource">
          <TextInput value={resource} onChange={(event) => setResource(event.target.value)} />
        </Field>
        <button type="submit" className="rounded-md border border-sky-700 bg-sky-900/40 px-3 py-1.5 text-sm text-sky-300">
          apply
        </button>
      </form>
      {audit.isPending ? (
        <LoadingNote label="Loading audit trail…" />
      ) : audit.isError ? (
        <ErrorNote error={audit.error} hint="GET /v1/audit failed — the token needs audit read scope." />
      ) : (audit.data ?? []).length === 0 ? (
        <EmptyNote label="No audit entries match." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Time", "Source", "Actor", "Action", "Resource", "Before → After"]} />
            <tbody>
              {(audit.data ?? []).map((entry) => (
                <tr key={`${entry.created_at}-${entry.action}-${entry.resource}`} className="border-b border-slate-800/60 align-top hover:bg-slate-900/40">
                  <td className="px-3 py-2 text-xs text-slate-500">{formatTime(entry.created_at)}</td>
                  <td className="px-3 py-2 text-xs text-slate-400">{entry.source}</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-400" title={entry.actor}>
                    {shortId(entry.actor)}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-200">{entry.action}</td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-400" title={entry.resource}>
                    {shortId(entry.resource)}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-slate-600">
                    {entry.before_fp ? <span title={entry.before_fp}>{entry.before_fp.slice(0, 8)}…</span> : "∅"} →{" "}
                    {entry.after_fp ? <span title={entry.after_fp}>{entry.after_fp.slice(0, 8)}…</span> : "∅"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </PageShell>
  );
}
