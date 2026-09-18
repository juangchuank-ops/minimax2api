import { useTranslation } from "react-i18next";

import { Tooltip } from "@/components/ui/tooltip";
import type { AccountQuota } from "@/features/accounts/accounts-api";
import { cn } from "@/shared/lib/cn";
import { formatDuration, formatRelative, hasInstant } from "@/shared/lib/format";

/**
 * MiniMax Agent exposes no credit balance through its web API, so a probe can
 * only report reachability and latency. That is still the useful signal: it
 * separates "the token is dead" from "the account is merely slow".
 */
export function AccountQuotaCell({ quota }: { quota: AccountQuota | null }) {
  const { t, i18n } = useTranslation();

  if (!quota) {
    return <span className="text-xs text-muted-foreground">{t("accounts.quotaUnsynced")}</span>;
  }

  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-center gap-2">
        <span className={cn("text-xs", quota.available ? "text-foreground" : "text-destructive")}>
          {quota.available ? t("accounts.probeSucceeded") : t("accounts.probeFailed")}
        </span>
        {quota.latencyMs > 0 ? (
          <span className="text-[10px] text-muted-foreground tabular-nums">{formatDuration(quota.latencyMs)}</span>
        ) : null}
      </div>
      {quota.plan ? (
        <p className="truncate text-[10px] text-muted-foreground" title={quota.plan}>
          {quota.plan}
        </p>
      ) : null}
      <Tooltip label={`${quota.note || "—"} · ${hasInstant(quota.syncedAt) ? formatRelative(quota.syncedAt, i18n.language) : ""}`}>
        <span className="block max-w-40 truncate text-[10px] text-muted-foreground">
          {hasInstant(quota.syncedAt) ? formatRelative(quota.syncedAt, i18n.language) : t("accounts.quotaUnsynced")}
        </span>
      </Tooltip>
    </div>
  );
}
