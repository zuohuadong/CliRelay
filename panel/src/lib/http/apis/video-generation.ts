import { apiClient } from "@/lib/http/client";

const VIDEO_GENERATION_TASK_POLL_TIMEOUT_MS = 10 * 1000;

export interface VideoGenerationTestRequest {
  model: string;
  prompt: string;
  seconds?: string;
  size?: string;
}

export interface VideoGenerationChannel {
  provider: string;
  model: string;
  type?: string;
}

export interface VideoGenerationChannelsResponse {
  items?: VideoGenerationChannel[];
}

export interface VideoGenerationTestResult {
  id?: string;
  object?: string;
  model?: string;
  status?: string;
  progress?: number;
  prompt?: string;
  seconds?: string | number;
  size?: string;
  video_url?: string;
  url?: string;
  video?: { url?: string };
  error?: {
    code?: string;
    message?: string;
  };
  [key: string]: unknown;
}

export type VideoGenerationTestTaskStatus = "queued" | "running" | "succeeded" | "failed";

export interface VideoGenerationTestTaskStartResponse {
  task_id: string;
  status: VideoGenerationTestTaskStatus;
  phase?: string;
  elapsed_ms?: number;
}

export interface VideoGenerationTestTaskResponse extends VideoGenerationTestTaskStartResponse {
  result?: VideoGenerationTestResult;
  error?: {
    status?: number;
    body?: {
      error?: {
        message?: string;
        type?: string;
        upstream?: unknown;
      };
    };
  };
}

export const videoGenerationApi = {
  getChannels: (): Promise<VideoGenerationChannelsResponse> => {
    return apiClient.get<VideoGenerationChannelsResponse>("/video-generation/channels");
  },

  startTestTask: (
    payload: VideoGenerationTestRequest,
  ): Promise<VideoGenerationTestTaskStartResponse> => {
    return apiClient.post<VideoGenerationTestTaskStartResponse>("/video-generation/test", payload);
  },

  getTestTask: (taskId: string): Promise<VideoGenerationTestTaskResponse> => {
    return apiClient.get<VideoGenerationTestTaskResponse>(
      `/video-generation/test/${encodeURIComponent(taskId)}`,
      {
        timeoutMs: VIDEO_GENERATION_TASK_POLL_TIMEOUT_MS,
      },
    );
  },
};
