import { createFileRoute } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";

import { BackLink } from "@/components/BackLink";
import { Item, ItemContent, ItemDescription, ItemGroup, ItemTitle } from "@/components/ui/item";

export const Route = createFileRoute("/sources")({
  component: SourcesComponent,
});

function SourcesComponent() {
  return (
    <main className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-6 px-4 py-12 sm:px-6">
      <BackLink to="/" />
      <header className="space-y-2">
        <h1 className="text-3xl font-semibold tracking-tight">Data sources</h1>
        <p className="text-sm text-muted-foreground">
          The model is trained on observations and forecasts from these two public sources.
        </p>
      </header>
      <ItemGroup>
        <SourceItem
          name="Open-Meteo"
          href="https://open-meteo.com/"
          description="Hourly historical-forecast features from the ECMWF IFS and UK Met Office UKMO global models, used as model inputs at both training and serving time."
        />
        <SourceItem
          name="COSMOS-UK"
          href="https://cosmos.ceh.ac.uk/"
          description="Research-grade weather station network operated by the UK Centre for Ecology & Hydrology. Provides the pluvio precipitation observations used as training labels."
        />
      </ItemGroup>
    </main>
  );
}

function SourceItem({
  name,
  href,
  description,
}: {
  name: string;
  href: string;
  description: string;
}) {
  return (
    <Item variant="outline" asChild>
      <a href={href} target="_blank" rel="noreferrer noopener">
        <ItemContent>
          <ItemTitle>
            {name}
            <ExternalLink className="h-4 w-4" aria-hidden="true" />
          </ItemTitle>
          <ItemDescription>{description}</ItemDescription>
        </ItemContent>
      </a>
    </Item>
  );
}
