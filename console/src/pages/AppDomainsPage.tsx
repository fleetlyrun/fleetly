// 域名管理：台账列表（服务/域名/端口/证书状态）+ verify 动作（本机视角
// 探测：DNS 解析 + 80/443 探测 + 实收证书诚实记录）。台账写入方唯一 =
// internal/ingress（部署声明路径），Console 只读不写。

import { useMutation, useQuery } from "@tanstack/react-query";
import { RefreshCw, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { useParams } from "react-router-dom";

import { listDomains, verifyDomains } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import {
  EnvelopeAlert,
} from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatTime } from "@/lib/utils";

export function AppDomainsPage() {
  const { name = "" } = useParams();
  const [verifyError, setVerifyError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  const query = useQuery({
    queryKey: ["domains", name],
    queryFn: () => listDomains(name),
  });
  const verifyMutation = useMutation({
    mutationFn: () => verifyDomains(name),
    onError: (err) => setVerifyError(errorEnvelopeFrom(err)),
  });

  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  const domains = query.data?.domains ?? [];
  const checks = verifyMutation.data?.checks ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex-row items-center justify-between space-y-0 pb-3">
          <div>
            <CardTitle className="text-sm font-medium text-muted-foreground">
              Domains
            </CardTitle>
            <CardDescription>
              Declared in compose; synced by the platform at release time.
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label="Refresh domains"
              onClick={() => void query.refetch()}
            >
              <RefreshCw aria-hidden className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setVerifyError(null);
                verifyMutation.mutate();
              }}
              disabled={verifyMutation.isPending}
            >
              <ShieldCheck aria-hidden className="h-3.5 w-3.5" />
              Verify
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {verifyError ? (
            <div className="mb-3">
              <EnvelopeAlert
                code={verifyError.code}
                message={verifyError.message}
                suggestion={verifyError.suggestion}
                docs={verifyError.docs}
              />
            </div>
          ) : null}
          {domains.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No domains declared for this app.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domain</TableHead>
                  <TableHead>Service</TableHead>
                  <TableHead>Port</TableHead>
                  <TableHead>Certificate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {domains.map((d) => (
                  <TableRow key={`${d.service}:${d.domain}`}>
                    <TableCell className="font-mono text-xs">{d.domain}</TableCell>
                    <TableCell>{d.service}</TableCell>
                    <TableCell>{d.port || <span className="text-muted-foreground">not synced</span>}</TableCell>
                    <TableCell className="text-xs">
                      {d.cert_sha256 ? (
                        <span className="space-y-0.5">
                          <div className="flex items-center gap-1 text-emerald-700">
                            <ShieldCheck aria-hidden className="h-3.5 w-3.5" />
                            issued · expires {formatTime(d.cert_not_after)}
                          </div>
                          <code className="text-[10px] text-muted-foreground">
                            sha256:{d.cert_sha256.slice(0, 16)}…
                          </code>
                        </span>
                      ) : (
                        <span className="text-muted-foreground">no certificate</span>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {verifyMutation.data ? (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm font-medium text-muted-foreground">
              Verification result (local probe — judgement is yours)
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domain</TableHead>
                  <TableHead>DNS</TableHead>
                  <TableHead>:80</TableHead>
                  <TableHead>:443</TableHead>
                  <TableHead>Served certificate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {checks.map((c) => (
                  <TableRow key={c.domain}>
                    <TableCell className="font-mono text-xs">{c.domain}</TableCell>
                    <TableCell className="text-xs">
                      {c.resolved ? c.ips.join(", ") : (
                        <span className="text-red-700">{c.error || "unresolved"}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs">{c.http_80 || "—"}</TableCell>
                    <TableCell className="text-xs">{c.https_443 || "—"}</TableCell>
                    <TableCell className="text-xs">
                      {c.cert_subject || "—"}
                      {c.cert_not_after
                        ? ` · expires ${formatTime(c.cert_not_after)}`
                        : ""}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}
    </div>
  );
}
