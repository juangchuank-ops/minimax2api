import { apiRequest } from "@/shared/api/client";

export type ModelDTO = {
  id: string;
  name: string;
  upstream: string;
  // The model identifier the upstream itself uses, when one exists. Only the
  // video entries have one: it is the value that goes into a generation
  // request, and it is not the same as `upstream`, which is a local dispatch
  // hint that never leaves this process.
  upstreamModel: string;
  type: "chat" | "image" | "video";
  // The generation values this model accepts. They differ between models — the
  // two H3 variants offer different resolutions — and a request naming a value
  // outside them is repaired rather than rejected, so this is the only place
  // the ranges can be read before spending a call to find out.
  //
  // Empty means unconstrained: the model's own parameter panel has not been
  // read, so the gateway does not enforce anything about it. The keys are
  // absent rather than empty on the entries that have no ranges at all, which
  // is every chat and image model — hence the optional marker.
  ratios?: string[];
  resolutions?: string[];
  durations?: number[];
  enabled: boolean;
  builtin: boolean;
  description: string;
  requests: number;
  tokens: number;
};

export function listModels(): Promise<{ items: ModelDTO[] }> {
  return apiRequest("/admin/api/models");
}

export function updateModel(id: string, payload: Partial<{ enabled: boolean; name: string; description: string }>): Promise<{ model: ModelDTO }> {
  return apiRequest(`/admin/api/models/${id}`, { method: "PATCH", body: payload });
}
