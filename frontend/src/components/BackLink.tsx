import { Link, type LinkProps } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";

import { cn } from "@/lib/utils";

type BackLinkProps = LinkProps & {
  className?: string;
  children?: React.ReactNode;
};

export function BackLink({ children = "Back", className, ...props }: BackLinkProps) {
  return (
    <Link
      {...props}
      className={cn(
        "inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground",
        className,
      )}
    >
      <ArrowLeft className="h-4 w-4" aria-hidden="true" />
      {children}
    </Link>
  );
}
