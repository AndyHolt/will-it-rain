import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { type PredictResponse, predictQueryOptions } from "@/forecast/fetch";

export const Route = createFileRoute("/")({
  loader: ({ context: { queryClient } }) => queryClient.ensureQueryData(predictQueryOptions()),
  component: IndexComponent,
});

function IndexComponent() {
  const { data, isPending, isError, error, refetch, isFetching } = useQuery(predictQueryOptions());

  return (
    <main className="mx-auto flex min-h-svh max-w-xl flex-col items-center justify-center gap-6 px-6 py-12">
      <h1 className="text-3xl font-semibold tracking-tight">Will it rain?</h1>

      {isPending && <p className="text-muted-foreground">Loading forecast…</p>}

      {isError && (
        <div className="w-full rounded-lg border border-destructive/40 bg-destructive/10 p-4 text-sm text-destructive">
          <p className="font-medium">Couldn't load the forecast.</p>
          <p className="mt-1 opacity-80">{(error as Error).message}</p>
          <button
            type="button"
            onClick={() => refetch()}
            className="mt-3 rounded-md border border-destructive/40 px-3 py-1 text-xs font-medium hover:bg-destructive/20"
          >
            Try again
          </button>
        </div>
      )}

      {data && <Prediction data={data} isRefreshing={isFetching} />}
    </main>
  );
}

function Prediction({ data, isRefreshing }: { data: PredictResponse; isRefreshing: boolean }) {
  const verdict = data.will_rain ? "Yes, bring a coat." : "No, you're probably fine.";
  const pct = Math.round(data.calibrated_prob * 100);

  return (
    <section className="w-full rounded-xl border border-border bg-card p-6 text-card-foreground shadow-sm">
      <p className="text-sm text-muted-foreground">
        Forecast for {formatRange(data.anchor_utc, data.window_end_utc)}
      </p>
      <p className="mt-3 text-4xl font-semibold">{verdict}</p>
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
