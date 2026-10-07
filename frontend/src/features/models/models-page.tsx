import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { listModels, updateModel, type ModelDTO } from "@/features/models/models-api";
import { errorMessage } from "@/shared/api/client";
import { DataTableShell } from "@/shared/components/data-table-shell";
import { EmptyState } from "@/shared/components/data-state";
import { PageHeader } from "@/shared/components/page-header";
import { formatNumber, formatTokens } from "@/shared/lib/format";

const TYPE_TONE: Record<string, string> = {
  chat: "bg-sky-500/10 text-sky-700 dark:text-sky-300",
  image: "bg-violet-500/10 text-violet-700 dark:text-violet-300",
  video: "bg-amber-500/10 text-amber-700 dark:text-amber-300",
};

// formatDurations collapses a contiguous run into a range.
//
// The panel offers every whole second from 5 to 15, and "5-15s" says the same
// thing as eleven numbers while fitting in a table cell. A gap — some models
// skip values — still has to be readable, so a non-contiguous set stays a list
// rather than becoming a range that claims values it does not accept.
function formatDurations(durations: number[], unit: string): string {
  const sorted = [...durations].sort((a, b) => a - b);
  const contiguous = sorted.every((value, index) => index === 0 || value === sorted[index - 1] + 1);
  if (sorted.length > 1 && contiguous) {
    return `${sorted[0]}-${sorted[sorted.length - 1]}${unit}`;
  }
  return sorted.map((value) => `${value}${unit}`).join(" / ");
}

// ModelRanges shows what a video model accepts.
//
// The ranges are per model and they differ — the two H3 variants disagree about
// resolution — so without this the only way to learn them is to make a call and
// read the repair report. A model whose panel has not been read shows nothing
// rather than an empty row, because "no ranges" means unenforced, not
// "accepts nothing".
function ModelRanges({ model }: { model: ModelDTO }) {
  const { t } = useTranslation();
  const resolutions = model.resolutions ?? [];
  const ratios = model.ratios ?? [];
  const durations = model.durations ?? [];
  const maxReferences = model.maxReferences ?? 0;
  if (resolutions.length === 0 && ratios.length === 0 && durations.length === 0 && maxReferences === 0) {
    return null;
  }
  const fields: Array<{ label: string; value: string }> = [];
  if (resolutions.length > 0) {
    fields.push({ label: t("models.resolutions"), value: resolutions.join(" / ") });
  }
  if (ratios.length > 0) {
    fields.push({ label: t("models.ratios"), value: ratios.join(" ") });
  }
  if (durations.length > 0) {
    fields.push({ label: t("models.durations"), value: formatDurations(durations, t("models.seconds")) });
  }
  // Only shown when there is a cap. "Uncapped" and "not read yet" are the same
  // state to the gateway — it refuses nothing in either — so the console does
  // not claim to tell them apart.
  if (maxReferences > 0) {
    fields.push({
      label: t("models.references"),
      value: t("models.referenceLimit", { count: maxReferences }),
    });
  }
  return (
    <p className="mt-1 flex flex-wrap gap-x-2.5 gap-y-0.5 text-[10px] text-muted-foreground/80">
      {fields.map((field) => (
        <span key={field.label}>
          {field.label} <span className="font-mono">{field.value}</span>
        </span>
      ))}
    </p>
  );
}

export function ModelsPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const modelsQuery = useQuery({ queryKey: ["models"], queryFn: listModels });

  const updateMutation = useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) => updateModel(id, { enabled }),
    onSuccess: () => {
      toast.success(t("models.saved"));
      void queryClient.invalidateQueries({ queryKey: ["models"] });
      void queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
    onError: (error) => toast.error(errorMessage(error)),
  });

  const items = modelsQuery.data?.items ?? [];

  return (
    <div className="space-y-5">
      <PageHeader title={t("models.title")} description={t("models.description")} />
      <DataTableShell
        toolbar={<span className="text-xs text-muted-foreground">{t("common.total", { count: items.length })}</span>}
      >
        {modelsQuery.isPending ? (
          <div className="flex h-64 items-center justify-center">
            <Spinner className="size-5" />
          </div>
        ) : items.length === 0 ? (
          <EmptyState label={t("common.empty")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("models.modelId")}</TableHead>
                <TableHead>{t("models.upstream")}</TableHead>
                <TableHead className="text-center">{t("models.type")}</TableHead>
                <TableHead>{t("models.displayName")}</TableHead>
                <TableHead className="text-center">{t("models.requests")}</TableHead>
                <TableHead className="text-center">{t("models.tokens")}</TableHead>
                <TableHead className="text-center">{t("models.status")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((model) => (
                <TableRow key={model.id}>
                  <TableCell className="font-mono text-xs">{model.id}</TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{model.upstream}</TableCell>
                  <TableCell className="text-center">
                    <span className={`inline-flex h-5 items-center rounded-full px-2 text-[11px] leading-none ${TYPE_TONE[model.type] ?? ""}`}>
                      {model.type === "chat" ? t("models.typeChat") : model.type === "image" ? t("models.typeImage") : t("models.typeVideo")}
                    </span>
                  </TableCell>
                  <TableCell className="text-xs">
                    <p>{model.name}</p>
                    <p className="mt-0.5 text-[10px] text-muted-foreground">{model.description}</p>
                    <ModelRanges model={model} />
                  </TableCell>
                  <TableCell className="text-center text-xs tabular-nums">{formatNumber(model.requests, i18n.language)}</TableCell>
                  <TableCell className="text-center text-xs tabular-nums">{formatTokens(model.tokens, i18n.language)}</TableCell>
                  <TableCell>
                    <div className="flex items-center justify-center gap-2">
                      {model.builtin ? <Badge variant="outline">{t("models.builtin")}</Badge> : null}
                      <Switch
                        checked={model.enabled}
                        onCheckedChange={(enabled) => updateMutation.mutate({ id: model.id, enabled })}
                      />
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </DataTableShell>
    </div>
  );
}
