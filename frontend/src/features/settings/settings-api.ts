import { apiRequest } from "@/shared/api/client";

export type SettingsDTO = {
  server: {
    addr: string;
    maxConcurrentRequests: number;
    adminUsername: string;
  };
  upstream: {
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
  };
  routing: {
    strategy: "least_inflight" | "round_robin" | "priority" | "random";
    cooldownBaseSec: number;
    cooldownMaxSec: number;
    maxAttempts: number;
    capacityWaitSec: number;
    stickyTTLSec: number;
    preferIdle: boolean;
  };
  audit: {
    retentionDays: number;
    maxRecords: number;
    recordBody: boolean;
    bodyLimitBytes: number;
  };
  media: {
    generatedDir: string;
    publicBaseURL: string;
    maxTotalSizeMB: number;
    autoDownload: boolean;
  };
  signin: {
    enabled: boolean;
    hour: number;
    minute: number;
    gapSeconds: number;
    timeoutSec: number;
    skipZeroCredit: boolean;
    creditFreshMin: number;
    creditRefreshMin: number;
    lang: string;
    osName: string;
    browserName: string;
    browserLanguage: string;
    browserPlatform: string;
    deviceMemory: number;
    cpuCoreNum: number;
    timezoneOffsetMin: number;
    statusPath: string;
    claimPath: string;
    creditPath: string;
    creditDetailsPath: string;
  };
  video: {
    pluginName: string;
    optionsTag: string;
    defaultRatio: string;
    defaultResolution: string;
    defaultDuration: number;
    timeoutSec: number;
  };
  about: {
    version: string;
    buildTime: string;
    dataDir: string;
    upstreamURL: string;
  };
};

export function getSettings(): Promise<SettingsDTO> {
  return apiRequest<SettingsDTO>("/admin/api/settings");
}

export function saveSettings(payload: Partial<SettingsDTO> & { adminPassword?: string }): Promise<SettingsDTO> {
  return apiRequest("/admin/api/settings", { method: "PUT", body: payload });
}
