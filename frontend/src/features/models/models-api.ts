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
