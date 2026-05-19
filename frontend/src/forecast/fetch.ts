import { queryOptions } from "@tanstack/react-query";

export interface PredictResponse {
  anchor_utc: string;
  window_end_utc: string;
  raw_prob: number;
  calibrated_prob: number;
  threshold: number;
  will_rain: boolean;
  model_version: string;
}

async function fetchPrediction(signal?: AbortSignal): Promise<PredictResponse> {
  const res = await fetch("/api/predict", { signal });
  if (!res.ok) {
    const detail = await res.text().catch(() => "");
    throw new Error(`Prediction request failed (${res.status})${detail ? `: ${detail}` : ""}`);
  }
  return (await res.json()) as PredictResponse;
}

export const predictQueryOptions = () =>
  queryOptions({
    queryKey: ["predict"],
    queryFn: ({ signal }) => fetchPrediction(signal),
    staleTime: 5 * 60 * 1000,
  });
