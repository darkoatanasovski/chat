"use client";

import { useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// One QueryClient per browser tab, created lazily inside useState so it
// survives re-renders but isn't shared across requests during SSR (the
// standard Next.js App Router caveat — a module-level singleton would leak
// one org's cached data into another request on the server). Every console
// page is itself a client component behind ConsoleShell's auth gate, so
// this provider only ever does real work in the browser.
//
// staleTime is deliberately non-zero: the whole point of switching the
// console over to react-query was to stop every page nav from blanking the
// screen back to skeletons while it refetches data it already has. A few
// seconds of staleness is an easy trade against that — mutations
// (updateApp, createCredential, etc.) invalidate their own queries
// explicitly, so a change made through the dashboard itself always shows up
// immediately regardless of staleTime.
function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        gcTime: 5 * 60_000,
        refetchOnWindowFocus: false,
        retry: 1,
      },
    },
  });
}

export function QueryProvider({ children }: { children: ReactNode }) {
  const [client] = useState(makeQueryClient);
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
