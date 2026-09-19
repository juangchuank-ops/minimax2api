import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarCheck, ExternalLink, RefreshCw, Server, Settings2, Shield, Sparkles, Waves, ScrollText } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { getSettings, saveSettings, type SettingsDTO } from "@/features/settings/settings-api";
import { errorMessage } from "@/shared/api/client";
import { ErrorState } from "@/shared/components/data-state";
import { PageHeader } from "@/shared/components/page-header";
import { cn } from "@/shared/lib/cn";

type Draft = {
  addr: string;
  maxConcurrentRequests: number;
  adminUsername: string;
  adminPassword: string;
  baseURL: string;
  baseURLCN: string;
  agentID: string;
  sessionPath: string;
  messagePath: string;
  userInfoPath: string;
  agentListPath: string;
  configPath: string;
  connectionsPath: string;
  modelPayload: string;
  language: string;
  screenWidth: number;
  screenHeight: number;
  requestTimeoutSec: number;
  streamIdleTimeoutSec: number;
  proxy: string;
  userAgent: string;
  strategy: SettingsDTO["routing"]["strategy"];
  cooldownBaseSec: number;
  cooldownMaxSec: number;
  maxAttempts: number;
  capacityWaitSec: number;
  stickyTTLSec: number;
  preferIdle: boolean;
  retentionDays: number;
  maxRecords: number;
  recordBody: boolean;
  bodyLimitBytes: number;
  generatedDir: string;
  publicBaseURL: string;
  maxTotalSizeMB: number;
  autoDownload: boolean;
  // The check-in fields are spelled out with a prefix rather than spread in
  // like the sections above. Two of them would otherwise be ambiguous next to
  // the upstream block: `lang` sits beside `language`, and `enabled` reads as
  // the whole gateway's switch rather than one section's.
  signinEnabled: boolean;
  signinHour: number;
  signinMinute: number;
  signinGapSeconds: number;
  signinTimeoutSec: number;
  signinSkipZeroCredit: boolean;
  signinCreditFreshMin: number;
  signinCreditRefreshMin: number;
  signinLang: string;
  signinOSName: string;
  signinBrowserName: string;
  signinBrowserLanguage: string;
  signinBrowserPlatform: string;
  signinDeviceMemory: number;
  signinCPUCoreNum: number;
  signinTimezoneOffsetMin: number;
  signinStatusPath: string;
  signinClaimPath: string;
  signinCreditPath: string;
  signinCreditDetailsPath: string;
};

function toDraft(settings: SettingsDTO): Draft {
  const signin = settings.signin;
  return {
    ...settings.server,
    adminPassword: "",
    ...settings.upstream,
    ...settings.routing,
    ...settings.audit,
    ...settings.media,
    signinEnabled: signin.enabled,
    signinHour: signin.hour,
    signinMinute: signin.minute,
    signinGapSeconds: signin.gapSeconds,
    signinTimeoutSec: signin.timeoutSec,
    signinSkipZeroCredit: signin.skipZeroCredit,
    signinCreditFreshMin: signin.creditFreshMin,
    signinCreditRefreshMin: signin.creditRefreshMin,
    signinLang: signin.lang,
    signinOSName: signin.osName,
    signinBrowserName: signin.browserName,
    signinBrowserLanguage: signin.browserLanguage,
    signinBrowserPlatform: signin.browserPlatform,
    signinDeviceMemory: signin.deviceMemory,
    signinCPUCoreNum: signin.cpuCoreNum,
    signinTimezoneOffsetMin: signin.timezoneOffsetMin,
    signinStatusPath: signin.statusPath,
    signinClaimPath: signin.claimPath,
    signinCreditPath: signin.creditPath,
    signinCreditDetailsPath: signin.creditDetailsPath,
  };
}

export function SettingsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const settingsQuery = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const [draft, setDraft] = useState<Draft | null>(null);

  useEffect(() => {
    if (settingsQuery.data) setDraft(toDraft(settingsQuery.data));
  }, [settingsQuery.data]);

  const saveMutation = useMutation({
    mutationFn: (value: Draft) =>
      saveSettings({
        server: {
          addr: value.addr,
          maxConcurrentRequests: value.maxConcurrentRequests,
          adminUsername: value.adminUsername,
        },
        upstream: {
          baseURL: value.baseURL,
          baseURLCN: value.baseURLCN,
          agentID: value.agentID,
          sessionPath: value.sessionPath,
          messagePath: value.messagePath,
          userInfoPath: value.userInfoPath,
          agentListPath: value.agentListPath,
          configPath: value.configPath,
          connectionsPath: value.connectionsPath,
          modelPayload: value.modelPayload,
          language: value.language,
          screenWidth: value.screenWidth,
          screenHeight: value.screenHeight,
          requestTimeoutSec: value.requestTimeoutSec,
          streamIdleTimeoutSec: value.streamIdleTimeoutSec,
          proxy: value.proxy,
          userAgent: value.userAgent,
        },
        routing: {
          strategy: value.strategy,
          cooldownBaseSec: value.cooldownBaseSec,
          cooldownMaxSec: value.cooldownMaxSec,
          maxAttempts: value.maxAttempts,
          capacityWaitSec: value.capacityWaitSec,
          stickyTTLSec: value.stickyTTLSec,
          preferIdle: value.preferIdle,
        },
        audit: {
          retentionDays: value.retentionDays,
          maxRecords: value.maxRecords,
          recordBody: value.recordBody,
          bodyLimitBytes: value.bodyLimitBytes,
        },
        media: {
          generatedDir: value.generatedDir,
          publicBaseURL: value.publicBaseURL,
          maxTotalSizeMB: value.maxTotalSizeMB,
          autoDownload: value.autoDownload,
        },
        signin: {
          enabled: value.signinEnabled,
          hour: value.signinHour,
          minute: value.signinMinute,
          gapSeconds: value.signinGapSeconds,
          timeoutSec: value.signinTimeoutSec,
          skipZeroCredit: value.signinSkipZeroCredit,
          creditFreshMin: value.signinCreditFreshMin,
          creditRefreshMin: value.signinCreditRefreshMin,
          lang: value.signinLang,
          osName: value.signinOSName,
          browserName: value.signinBrowserName,
          browserLanguage: value.signinBrowserLanguage,
          browserPlatform: value.signinBrowserPlatform,
          deviceMemory: value.signinDeviceMemory,
          cpuCoreNum: value.signinCPUCoreNum,
          timezoneOffsetMin: value.signinTimezoneOffsetMin,
          statusPath: value.signinStatusPath,
          claimPath: value.signinClaimPath,
          creditPath: value.signinCreditPath,
          creditDetailsPath: value.signinCreditDetailsPath,
        },
        ...(value.adminPassword ? { adminPassword: value.adminPassword } : {}),
      }),
    onSuccess: () => {
      toast.success(t("settings.saved"));
      void queryClient.invalidateQueries({ queryKey: ["settings"] });
    },
    onError: (error) => toast.error(errorMessage(error)),
  });

  if (settingsQuery.isError && !settingsQuery.data) {
    return <ErrorState message={(settingsQuery.error as Error).message} onRetry={() => void settingsQuery.refetch()} />;
  }

  if (!draft) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Spinner className="size-5" />
      </div>
    );
  }

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((current) => (current ? { ...current, [key]: value } : current));
  const about = settingsQuery.data?.about;

  return (
    <div className="space-y-5">
      <PageHeader
        title={t("settings.title")}
        description={t("settings.description")}
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => void settingsQuery.refetch()}>
              <RefreshCw />
              {t("common.refresh")}
            </Button>
            <Button size="sm" disabled={saveMutation.isPending} onClick={() => saveMutation.mutate(draft)}>
              {t("common.save")}
            </Button>
          </>
        }
      />

      <div className="space-y-2">
        <SettingsGroup icon={<Server />} title={t("settings.groups.server")}>
          <Field label={t("settings.server.addr")} help={t("settings.server.addrHelp")}>
            <Input value={draft.addr} onChange={(event) => set("addr", event.target.value)} />
          </Field>
          <Field label={t("settings.server.maxConcurrentRequests")} help={t("settings.server.maxConcurrentRequestsHelp")}>
            <Input
              type="number"
              min={1}
              max={4096}
              value={draft.maxConcurrentRequests}
              onChange={(event) => set("maxConcurrentRequests", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.server.adminUsername")}>
            <Input value={draft.adminUsername} onChange={(event) => set("adminUsername", event.target.value)} />
          </Field>
          <Field label={t("settings.server.adminPassword")} help={t("settings.server.adminPasswordHelp")}>
            <Input
              type="password"
              autoComplete="new-password"
              value={draft.adminPassword}
              placeholder="••••••••"
              onChange={(event) => set("adminPassword", event.target.value)}
            />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<Waves />} title={t("settings.groups.upstream")}>
          <Field label={t("settings.upstream.baseURL")} help={t("settings.upstream.baseURLHelp")}>
            <Input value={draft.baseURL} onChange={(event) => set("baseURL", event.target.value)} />
          </Field>
          <Field label={t("settings.upstream.baseURLCN")} help={t("settings.upstream.baseURLCNHelp")}>
            <Input value={draft.baseURLCN} onChange={(event) => set("baseURLCN", event.target.value)} />
          </Field>
          <Field label={t("settings.upstream.agentId")} help={t("settings.upstream.agentIdHelp")}>
            <Input value={draft.agentID} onChange={(event) => set("agentID", event.target.value)} />
          </Field>
          <Field label={t("settings.upstream.sessionPath")} help={t("settings.upstream.sessionPathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.sessionPath}
              onChange={(event) => set("sessionPath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.messagePath")} help={t("settings.upstream.messagePathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.messagePath}
              onChange={(event) => set("messagePath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.userInfoPath")} help={t("settings.upstream.userInfoPathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.userInfoPath}
              onChange={(event) => set("userInfoPath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.agentListPath")} help={t("settings.upstream.agentListPathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.agentListPath}
              onChange={(event) => set("agentListPath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.configPath")} help={t("settings.upstream.configPathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.configPath}
              onChange={(event) => set("configPath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.connectionsPath")} help={t("settings.upstream.connectionsPathHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.connectionsPath}
              onChange={(event) => set("connectionsPath", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.modelPayload")} help={t("settings.upstream.modelPayloadHelp")}>
            <Input
              className="font-mono text-[11px]"
              value={draft.modelPayload}
              placeholder='{"id": "MiniMax-M3"}'
              onChange={(event) => set("modelPayload", event.target.value)}
            />
          </Field>
          <Field label={t("settings.upstream.screenWidth")} help={t("settings.upstream.screenHelp")}>
            <Input
              type="number"
              min={320}
              max={7680}
              value={draft.screenWidth}
              onChange={(event) => set("screenWidth", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.upstream.screenHeight")}>
            <Input
              type="number"
              min={240}
              max={4320}
              value={draft.screenHeight}
              onChange={(event) => set("screenHeight", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.upstream.language")}>
            <Input value={draft.language} onChange={(event) => set("language", event.target.value)} />
          </Field>
          <Field label={t("settings.upstream.requestTimeout")} unit={t("settings.units.seconds")}>
            <Input
              type="number"
              min={5}
              max={1800}
              value={draft.requestTimeoutSec}
              onChange={(event) => set("requestTimeoutSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.upstream.streamIdleTimeout")} unit={t("settings.units.seconds")} help={t("settings.upstream.streamIdleTimeoutHelp")}>
            <Input
              type="number"
              min={5}
              max={600}
              value={draft.streamIdleTimeoutSec}
              onChange={(event) => set("streamIdleTimeoutSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.upstream.proxy")} help={t("settings.upstream.proxyHelp")}>
            <Input value={draft.proxy} placeholder="http://127.0.0.1:7890" onChange={(event) => set("proxy", event.target.value)} />
          </Field>
          <Field label={t("settings.upstream.userAgent")} help={t("settings.upstream.userAgentHelp")}>
            <Input value={draft.userAgent} onChange={(event) => set("userAgent", event.target.value)} />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<Shield />} title={t("settings.groups.routing")}>
          <Field label={t("settings.routing.strategy")} help={t("settings.routing.strategyHelp")}>
            <Select
              value={draft.strategy}
              onChange={(value) => set("strategy", value as Draft["strategy"])}
              options={[
                { value: "least_inflight", label: t("settings.routing.strategyLeastInflight") },
                { value: "round_robin", label: t("settings.routing.strategyRoundRobin") },
                { value: "priority", label: t("settings.routing.strategyPriority") },
                { value: "random", label: t("settings.routing.strategyRandom") },
              ]}
            />
          </Field>
          <Field label={t("settings.routing.cooldownBase")} unit={t("settings.units.seconds")} help={t("settings.routing.cooldownBaseHelp")}>
            <Input
              type="number"
              min={1}
              max={3600}
              value={draft.cooldownBaseSec}
              onChange={(event) => set("cooldownBaseSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.routing.cooldownMax")} unit={t("settings.units.seconds")} help={t("settings.routing.cooldownMaxHelp")}>
            <Input
              type="number"
              min={1}
              max={86400}
              value={draft.cooldownMaxSec}
              onChange={(event) => set("cooldownMaxSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.routing.maxAttempts")} help={t("settings.routing.maxAttemptsHelp")}>
            <Input
              type="number"
              min={1}
              max={20}
              value={draft.maxAttempts}
              onChange={(event) => set("maxAttempts", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.routing.capacityWait")} unit={t("settings.units.seconds")} help={t("settings.routing.capacityWaitHelp")}>
            <Input
              type="number"
              min={0}
              max={300}
              value={draft.capacityWaitSec}
              onChange={(event) => set("capacityWaitSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.routing.stickyTTL")} unit={t("settings.units.seconds")} help={t("settings.routing.stickyTTLHelp")}>
            <Input
              type="number"
              min={0}
              max={3600}
              value={draft.stickyTTLSec}
              onChange={(event) => set("stickyTTLSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.routing.preferFast")} help={t("settings.routing.preferFastHelp")}>
            <Switch checked={draft.preferIdle} onCheckedChange={(value) => set("preferIdle", value)} />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<ScrollText />} title={t("settings.groups.audit")}>
          <Field label={t("settings.audit.retentionDays")} unit={t("settings.units.days")} help={t("settings.audit.retentionDaysHelp")}>
            <Input
              type="number"
              min={0}
              max={365}
              value={draft.retentionDays}
              onChange={(event) => set("retentionDays", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.audit.maxRecords")} help={t("settings.audit.maxRecordsHelp")}>
            <Input
              type="number"
              min={100}
              max={200000}
              value={draft.maxRecords}
              onChange={(event) => set("maxRecords", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.audit.recordBody")} help={t("settings.audit.recordBodyHelp")}>
            <Switch checked={draft.recordBody} onCheckedChange={(value) => set("recordBody", value)} />
          </Field>
          <Field label={t("settings.audit.bodyLimit")} help={t("settings.audit.bodyLimitHelp")}>
            <Input
              type="number"
              min={256}
              max={1048576}
              value={draft.bodyLimitBytes}
              onChange={(event) => set("bodyLimitBytes", Number(event.target.value))}
            />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<Sparkles />} title={t("settings.groups.media")}>
          <Field label={t("settings.media.generatedDir")} help={t("settings.media.generatedDirHelp")}>
            <Input value={draft.generatedDir} onChange={(event) => set("generatedDir", event.target.value)} />
          </Field>
          <Field label={t("settings.media.publicBaseURL")} help={t("settings.media.publicBaseURLHelp")}>
            <Input
              value={draft.publicBaseURL}
              placeholder="http://127.0.0.1:8080"
              onChange={(event) => set("publicBaseURL", event.target.value)}
            />
          </Field>
          <Field label={t("settings.media.maxTotalSize")} unit={t("settings.units.megabytes")} help={t("settings.media.maxTotalSizeHelp")}>
            <Input
              type="number"
              min={64}
              max={102400}
              value={draft.maxTotalSizeMB}
              onChange={(event) => set("maxTotalSizeMB", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.media.autoDownload")} help={t("settings.media.autoDownloadHelp")}>
            <Switch checked={draft.autoDownload} onCheckedChange={(value) => set("autoDownload", value)} />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<CalendarCheck />} title={t("settings.groups.signin")}>
          <Field label={t("settings.signin.enabled")} help={t("settings.signin.enabledHelp")}>
            <Switch checked={draft.signinEnabled} onCheckedChange={(value) => set("signinEnabled", value)} />
          </Field>
          <Field label={t("settings.signin.time")} help={t("settings.signin.timeHelp")}>
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={0}
                max={23}
                aria-label={t("settings.signin.time")}
                className="w-20"
                value={draft.signinHour}
                onChange={(event) => set("signinHour", Number(event.target.value))}
              />
              <span className="text-xs text-muted-foreground">:</span>
              <Input
                type="number"
                min={0}
                max={59}
                aria-label={t("settings.signin.time")}
                className="w-20"
                value={draft.signinMinute}
                onChange={(event) => set("signinMinute", Number(event.target.value))}
              />
            </div>
          </Field>
          <Field label={t("settings.signin.gap")} unit={t("settings.units.seconds")} help={t("settings.signin.gapHelp")}>
            <Input
              type="number"
              min={0}
              max={120}
              value={draft.signinGapSeconds}
              onChange={(event) => set("signinGapSeconds", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.signin.timeout")} unit={t("settings.units.seconds")} help={t("settings.signin.timeoutHelp")}>
            <Input
              type="number"
              min={5}
              max={300}
              value={draft.signinTimeoutSec}
              onChange={(event) => set("signinTimeoutSec", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.signin.skipZeroCredit")} help={t("settings.signin.skipZeroCreditHelp")}>
            <Switch checked={draft.signinSkipZeroCredit} onCheckedChange={(value) => set("signinSkipZeroCredit", value)} />
          </Field>
          <Field label={t("settings.signin.creditFresh")} unit={t("settings.units.minutes")} help={t("settings.signin.creditFreshHelp")}>
            <Input
              type="number"
              min={1}
              max={10080}
              value={draft.signinCreditFreshMin}
              onChange={(event) => set("signinCreditFreshMin", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.signin.creditRefresh")} unit={t("settings.units.minutes")} help={t("settings.signin.creditRefreshHelp")}>
            <Input
              type="number"
              min={0}
              max={1440}
              value={draft.signinCreditRefreshMin}
              onChange={(event) => set("signinCreditRefreshMin", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.signin.lang")} help={t("settings.signin.langHelp")}>
            <Input value={draft.signinLang} onChange={(event) => set("signinLang", event.target.value)} />
          </Field>
          <Field label={t("settings.signin.timezoneOffset")} help={t("settings.signin.timezoneOffsetHelp")}>
            <Input
              type="number"
              min={-720}
              max={840}
              value={draft.signinTimezoneOffsetMin}
              onChange={(event) => set("signinTimezoneOffsetMin", Number(event.target.value))}
            />
          </Field>
          <Field label={t("settings.signin.statusPath")} help={t("settings.signin.pathHelp")}>
            <Input value={draft.signinStatusPath} onChange={(event) => set("signinStatusPath", event.target.value)} />
          </Field>
          <Field label={t("settings.signin.claimPath")} help={t("settings.signin.pathHelp")}>
            <Input value={draft.signinClaimPath} onChange={(event) => set("signinClaimPath", event.target.value)} />
          </Field>
          <Field label={t("settings.signin.creditPath")} help={t("settings.signin.pathHelp")}>
            <Input value={draft.signinCreditPath} onChange={(event) => set("signinCreditPath", event.target.value)} />
          </Field>
          <Field
            label={t("settings.signin.creditDetailsPath")}
            help={t("settings.signin.creditDetailsPathHelp")}
          >
            <Input
              className="font-mono text-[11px]"
              value={draft.signinCreditDetailsPath}
              onChange={(event) => set("signinCreditDetailsPath", event.target.value)}
            />
          </Field>
        </SettingsGroup>

        <SettingsGroup icon={<Settings2 />} title={t("settings.groups.about")}>
          <Field label={t("settings.about.version")}>
            <span className="font-mono text-xs text-muted-foreground">{about?.version ?? "—"}</span>
          </Field>
          <Field label={t("settings.about.buildTime")}>
            <span className="font-mono text-xs text-muted-foreground">{about?.buildTime ?? "—"}</span>
          </Field>
          <Field label={t("settings.about.dataDir")}>
            <span className="font-mono text-xs text-muted-foreground">{about?.dataDir ?? "—"}</span>
          </Field>
          <Field label={t("settings.about.upstreamURL")}>
            <a
              href={about?.upstreamURL ?? "https://www.minimax.com/"}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              {about?.upstreamURL ?? "https://www.minimax.com/"}
              <ExternalLink className="size-3" />
            </a>
          </Field>
        </SettingsGroup>
      </div>
    </div>
  );
}

function SettingsGroup({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg bg-card p-4">
      <header className="flex items-center gap-2">
        <span className="text-muted-foreground [&_svg]:size-4">{icon}</span>
        <h2 className="text-xs font-medium">{title}</h2>
      </header>
      <div className="mt-4 grid gap-x-8 gap-y-5 lg:grid-cols-2">{children}</div>
    </section>
  );
}

function Field({
  label,
  help,
  unit,
  children,
}: {
  label: string;
  help?: string;
  unit?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0 space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <Label className="min-w-0 truncate">{label}</Label>
        {unit ? <span className="shrink-0 text-[10px] text-muted-foreground">{unit}</span> : null}
      </div>
      <div className={cn("[&>input]:w-full [&>div]:w-full")}>{children}</div>
      {help ? <p className="text-[11px] leading-5 text-muted-foreground">{help}</p> : null}
    </div>
  );
}
