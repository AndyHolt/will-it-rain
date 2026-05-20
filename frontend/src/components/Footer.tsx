import { SiGithub } from "@icons-pack/react-simple-icons";
import { Link } from "@tanstack/react-router";

export function Footer() {
  return (
    <footer className="border-t border-border">
      <div className="mx-auto flex max-w-2xl flex-col items-center justify-between gap-2 px-4 py-6 text-sm text-muted-foreground sm:flex-row sm:px-6">
        <p>
          By{" "}
          <a
            href="https://github.com/AndyHolt"
            target="_blank"
            rel="noreferrer noopener"
            className="font-medium text-foreground hover:underline"
          >
            Andy Holt
          </a>
        </p>
        <nav className="flex items-center gap-4">
          <Link to="/sources" className="hover:text-foreground hover:underline">
            Data sources
          </Link>
          <a
            href="https://github.com/AndyHolt/will-it-rain"
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1.5 hover:text-foreground hover:underline"
          >
            <SiGithub className="h-4 w-4" aria-hidden />
            <span>GitHub</span>
          </a>
        </nav>
      </div>
    </footer>
  );
}
