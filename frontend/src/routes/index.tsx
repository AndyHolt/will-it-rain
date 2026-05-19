import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, type ErrorComponentProps } from "@tanstack/react-router";
import { Cloud, CloudRain } from "lucide-react";

import { type PredictResponse, predictQueryOptions } from "@/forecast/fetch";

export const Route = createFileRoute("/")({
  loader: ({ context: { queryClient } }) => queryClient.ensureQueryData(predictQueryOptions()),
  component: IndexComponent,
  pendingComponent: PendingComponent,
  errorComponent: ErrorComponent,
  pendingMs: 0,
});

function Shell({ children, status }: { children: React.ReactNode; status?: string }) {
  return (
    <main className="mx-auto flex min-h-svh max-w-xl flex-col items-center justify-center gap-6 px-6 py-12">
      <h1 className="text-3xl font-semibold tracking-tight">Will it rain?</h1>
      {children}
      <p
        className={`text-sm text-muted-foreground ${status ? "" : "invisible"}`}
        aria-hidden={status ? undefined : true}
      >
        {status ?? " "}
      </p>
    </main>
  );
}

function IndexComponent() {
  const { data, isFetching } = useSuspenseQuery(predictQueryOptions());

  return (
    <Shell>
      <Prediction data={data} isRefreshing={isFetching} />
    </Shell>
  );
}

function PendingComponent() {
  return (
    <Shell status="Fetching the latest forecast…">
      <section
        aria-busy="true"
        aria-live="polite"
        className="w-full rounded-xl border border-border bg-card p-6 text-card-foreground shadow-sm"
      >
        <div className="h-4 w-40 animate-pulse rounded bg-muted" />
        <div className="mt-3 flex items-center gap-3">
          <div className="h-10 w-10 shrink-0 animate-pulse rounded-full bg-muted" />
          <div className="h-9 w-56 animate-pulse rounded bg-muted" />
        </div>
        <dl className="mt-6 grid grid-cols-2 gap-4">
          <SkeletonStat />
          <SkeletonStat />
          <SkeletonStat />
          <SkeletonStat />
        </dl>
      </section>
    </Shell>
  );
}

function SkeletonStat() {
  return (
    <div>
      <div className="h-3 w-24 animate-pulse rounded bg-muted" />
      <div className="mt-1.5 h-4 w-16 animate-pulse rounded bg-muted" />
    </div>
  );
}

function ErrorComponent({ error, reset }: ErrorComponentProps) {
  return (
    <Shell>
      <div
        role="alert"
        className="w-full rounded-lg border border-destructive/40 bg-destructive/10 p-4 text-sm text-destructive"
      >
        <p className="font-medium">Couldn't load the forecast.</p>
        <p className="mt-1 opacity-80">{error.message}</p>
        <button
          type="button"
          onClick={reset}
          className="mt-3 rounded-md border border-destructive/40 px-3 py-1 text-xs font-medium hover:bg-destructive/20"
        >
          Try again
        </button>
      </div>
    </Shell>
  );
}

function Prediction({ data, isRefreshing }: { data: PredictResponse; isRefreshing: boolean }) {
  const verdict = data.will_rain ? "Yes, bring a coat." : "No, you're probably fine.";
  const pct = Math.round(data.calibrated_prob * 100);
  const Icon = data.will_rain ? CloudRain : Cloud;

  return (
    <section className="w-full rounded-xl border border-border bg-card p-6 text-card-foreground shadow-sm">
      <p className="text-sm text-muted-foreground">
        Forecast for {formatRange(data.anchor_utc, data.window_end_utc)}
      </p>
      <div className="mt-3 flex items-center gap-3">
        <Icon className="h-10 w-10 shrink-0" aria-hidden="true" strokeWidth={2} />
        <p className="text-4xl font-semibold">{verdict}</p>
      </div>
      <dl className="mt-6 grid grid-cols-2 gap-4 text-sm">
        <Stat label="Rain probability" value={`${pct}%`} />
        <Stat label="Decision threshold" value={`${Math.round(data.threshold * 100)}%`} />
        <Stat label="Model version" value={data.model_version} />
        <Stat label="Status" value={isRefreshing ? "Refreshing…" : "Up to date"} />
      </dl>
    </section>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 font-medium">{value}</dd>
    </div>
  );
}

function formatRange(startIso: string, endIso: string) {
  const start = new Date(startIso);
  const end = new Date(endIso);
  const fmt: Intl.DateTimeFormatOptions = {
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  };
  return `${start.toLocaleString(undefined, fmt)} – ${end.toLocaleString(undefined, fmt)}`;
}
