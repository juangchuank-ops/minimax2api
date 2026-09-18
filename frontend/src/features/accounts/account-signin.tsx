import { useTranslation } from "react-i18next";

import { Tooltip } from "@/components/ui/tooltip";
import type { AccountDTO, AccountSigninStatus } from "@/features/accounts/accounts-api";
import { cn } from "@/shared/lib/cn";
import { formatRelative, hasInstant } from "@/shared/lib/format";

/** Status values the upstream uses inside the seven-day panel. */
const DAY_CLAIMED = 3;

const STATUS_TONE: Record<AccountSigninStatus, string> = {
  "": "text-muted-foreground",
  ok: "text-emerald-500",
  already: "text-muted-foreground",
  failed: "text-destructive",
  skipped: "text-amber-500",
};

const STATUS_LABEL: Record<AccountSigninStatus, string> = {
  "": "accounts.signinNever",
  ok: "accounts.signinOk",
  already: "accounts.signinAlready",
  failed: "accounts.signinFailed",
  skipped: "accounts.signinSkipped",
};

/**
 * Daily check-in state for one account.
 *
 * The seven-day strip is rendered from the panel the backend cached during the
 * last sweep rather than fetched per row: the table shows many accounts at once
 * and the panel only changes once a day.
 */
export function AccountSigninCell({ account }: { account: AccountDTO }) {
  const { t, i18n } = useTranslation();
  const status = account.signinStatus;
  const days = account.signinPanel?.days ?? [];

  const detail = [
    hasInstant(account.signinAt)
      ? `${t("accounts.signinLastAttempt")} ${formatRelative(account.signinAt, i18n.language)}`
      : t("accounts.signinNever"),
    account.signinTotal > 0 ? `${t("accounts.signinTotalPoints")} ${account.signinTotal}` : "",
    account.signinError,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-center gap-2">
        <span className={cn("whitespace-nowrap text-xs", STATUS_TONE[status] ?? "text-muted-foreground")}>
          {t(STATUS_LABEL[status] ?? "accounts.signinNever")}
        </span>
        {account.signinStreak > 0 ? (
          <span className="text-[10px] text-muted-foreground tabular-nums">
            {t("accounts.signinDay", { day: account.signinStreak })}
          </span>
        ) : null}
      </div>

      {days.length > 0 ? (
        <Tooltip label={days.map((day) => `${t("accounts.signinDay", { day: day.dayNo })} ${day.points}`).join(" · ")}>
          <span className="flex items-center gap-0.5">
            {days.map((day) => (
              <span
                key={day.dayNo}
                className={cn(
                  "size-1.5 rounded-full",
                  day.status === DAY_CLAIMED ? "bg-emerald-500" : "bg-muted-foreground/30",
                  day.isToday && "ring-1 ring-foreground/40 ring-offset-1 ring-offset-background",
                )}
              />
            ))}
          </span>
        </Tooltip>
      ) : null}

      <Tooltip label={detail || "—"}>
        <span className="block max-w-40 truncate text-[10px] text-muted-foreground">
          {hasInstant(account.signinAt)
            ? formatRelative(account.signinAt, i18n.language)
            : t("accounts.signinNever")}
        </span>
      </Tooltip>
    </div>
  );
}

/**
 * Credit balance.
 *
 * A zero is shown in the destructive tone only when the routing guard is
 * actually acting on it, which the caller signals by passing `enforced`. A zero
 * that routing is ignoring is not a problem the operator needs to chase.
 */
export function AccountCreditCell({ account, enforced }: { account: AccountDTO; enforced: boolean }) {
  const { t, i18n } = useTranslation();
  const credit = account.credit;

  if (!credit) {
    return <span className="text-xs text-muted-foreground">{t("accounts.creditUnknown")}</span>;
  }

  const spent = credit.total <= 0;

  return (
    <div className="min-w-0 space-y-1">
      <span
        className={cn(
          "text-xs tabular-nums",
          spent && enforced ? "text-destructive" : "text-foreground",
        )}
      >
        {credit.total}
      </span>
      {credit.total > 0 ? (
        <p className="text-[10px] text-muted-foreground tabular-nums">
          {t("accounts.creditBreakdown", { free: credit.free, purchased: credit.purchased })}
        </p>
      ) : null}
      <Tooltip label={spent && enforced ? t("accounts.creditSkipped") : credit.planName || "—"}>
        <span className="block truncate text-[10px] text-muted-foreground">
          {formatRelative(credit.syncedAt, i18n.language)}
        </span>
      </Tooltip>
    </div>
  );
}
