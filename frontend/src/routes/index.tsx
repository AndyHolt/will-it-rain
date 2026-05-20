import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, type ErrorComponentProps } from "@tanstack/react-router";
import { ChevronDown, Cloud, CloudRain } from "lucide-react";

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
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
    <main className="mx-auto flex w-full max-w-2xl flex-1 flex-col items-center justify-center gap-6 px-4 py-12 sm:px-6">
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
        className="w-full rounded-xl border border-border bg-card p-4 text-card-foreground shadow-sm sm:p-6"
      >
        <Skeleton className="h-4 w-40" />
        <div className="mt-3 flex items-center gap-3 sm:gap-5">
          <Skeleton className="h-10 w-10 shrink-0 rounded-full" />
          <Skeleton className="h-9 max-w-full min-w-0 flex-1 sm:w-56 sm:flex-none" />
        </div>
        <Skeleton className="mt-6 h-5 w-16" />
      </section>
    </Shell>
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
    <section className="w-full rounded-xl border border-border bg-card p-4 text-card-foreground shadow-sm sm:p-6">
      <p className="text-sm text-muted-foreground">
        Forecast for {formatRange(data.anchor_utc, data.window_end_utc)}
      </p>
      <div className="mt-3 flex items-center gap-3 sm:gap-5">
        <Icon className="h-10 w-10 shrink-0" aria-hidden="true" strokeWidth={2} />
        <p className="min-w-0 text-3xl font-semibold break-words sm:text-4xl">{verdict}</p>
      </div>
      <Collapsible className="mt-6">
        <CollapsibleTrigger className="group flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
          Details
          <ChevronDown
            className="h-4 w-4 transition-transform group-data-[state=open]:rotate-180"
            aria-hidden="true"
          />
        </CollapsibleTrigger>
        <CollapsibleContent>
          <dl className="mt-4 grid grid-cols-1 gap-4 text-sm sm:grid-cols-2">
            <Stat label="Rain probability" value={`${pct}%`} />
            <Stat label="Decision threshold" value={`${Math.round(data.threshold * 100)}%`} />
            <Stat label="Model version" value={data.model_version} />
            <Stat label="Status" value={isRefreshing ? "Refreshing…" : "Up to date"} />
          </dl>
        </CollapsibleContent>
      </Collapsible>
    </section>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 font-medium break-words">{value}</dd>
    </div>
  );
}

function formatRange(startIso: string, endIso: string) {
  const start = new Date(startIso);
  const end = new Date(endIso);
  const timeFmt: Intl.DateTimeFormatOptions = {
    hour: "2-digit",
    minute: "2-digit",
  };
  const dayTimeFmt: Intl.DateTimeFormatOptions = {
    weekday: "short",
    ...timeFmt,
  };
  const sameDay =
    start.getFullYear() === end.getFullYear() &&
    start.getMonth() === end.getMonth() &&
    start.getDate() === end.getDate();
  const startStr = start.toLocaleString(undefined, dayTimeFmt);
  const endStr = end.toLocaleString(undefined, sameDay ? timeFmt : dayTimeFmt);
  return `${startStr} – ${endStr}`;
}
