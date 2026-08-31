"use client";

import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useRouter } from "next/navigation";
import { CornerDownLeft, Search as SearchIcon, X } from "lucide-react";
import { useAppsQuery } from "@/lib/queries";
import { buildAppIndex, buildStaticIndex, type SearchItem } from "@/lib/search-index";
import { useSession } from "./shell";
import { cx } from "./ui";

const MAX_RESULTS = 20;
const MAX_DEFAULT = 8;

/** Global "⌘K" command palette, docked next to the org name in the top bar.
 * Every page, per-app tab, and per-app setting (all 19 channel capabilities
 * plus message editing / max length / thread depth / dynamic partitioning /
 * commands / polls) is a single flat searchable index — see
 * lib/search-index.ts for how each href is built and how the app detail
 * page consumes the `setting` query param to scroll to and flash the
 * matching row. */
export function GlobalSearch() {
  const { session } = useSession();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  // Shares the exact same ["apps", orgId] query the Apps grid and app
  // detail page use — by the time someone opens the palette they've
  // usually already visited one of those, so this is normally an instant
  // cache hit rather than a fresh fetch. `enabled: open` still means a
  // session that never opens search never pays for the request at all.
  const appsQuery = useAppsQuery(session.token, session.org.org_id, open);
  const apps = appsQuery.data ?? null;

  useEffect(() => {
    function onKey(e: globalThis.KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((o) => !o);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    const t = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(t);
  }, [open]);

  const index = useMemo(() => [...buildStaticIndex(), ...buildAppIndex(apps ?? [])], [apps]);

  const results = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return index.slice(0, MAX_DEFAULT);
    return index.filter((item) => item.keywords.includes(q)).slice(0, MAX_RESULTS);
  }, [index, query]);

  useEffect(() => setActive(0), [results.length, query]);

  function go(item: SearchItem) {
    setOpen(false);
    router.push(item.href);
  }

  function onInputKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => Math.min(a + 1, results.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const item = results[active];
      if (item) go(item);
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  }

  const grouped = useMemo(() => groupBySection(results), [results]);
  const indexById = useMemo(() => new Map(results.map((item, i) => [item.id, i])), [results]);

  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className="flex w-64 items-center gap-2.5 rounded-xl border border-border-soft bg-surface-2/40 px-3.5 py-2 text-left text-sm text-text-faint transition-colors duration-150 hover:border-border hover:text-text-muted"
      >
        <SearchIcon className="h-4 w-4 shrink-0" strokeWidth={2} />
        <span className="flex-1 truncate">Search settings…</span>
        <kbd className="rounded-md border border-border-soft bg-surface px-1.5 py-0.5 font-mono text-[10px] text-text-faint">
          ⌘K
        </kbd>
      </button>

      {open && (
        <div className="fixed inset-0 z-50 flex items-start justify-center px-4 pt-[12vh]">
          <div className="animate-overlay-in absolute inset-0 bg-black/70 backdrop-blur-sm" onClick={() => setOpen(false)} />
          <div className="animate-modal-in relative flex max-h-[70vh] w-full max-w-xl flex-col overflow-hidden rounded-2xl border border-border bg-surface shadow-2xl">
            <div className="flex shrink-0 items-center gap-3 border-b border-border-soft px-4 py-3.5">
              <SearchIcon className="h-4.5 w-4.5 shrink-0 text-text-faint" strokeWidth={2} />
              <input
                ref={inputRef}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={onInputKeyDown}
                placeholder="Search pages, apps, and settings…"
                className="min-w-0 flex-1 bg-transparent text-[15px] text-text placeholder:text-text-faint focus:outline-none"
              />
              <button
                onClick={() => setOpen(false)}
                className="rounded-lg p-1 text-text-faint transition-colors duration-150 hover:text-text"
                aria-label="Close"
              >
                <X className="h-4 w-4" />
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto py-2">
              {apps === null ? (
                <div className="px-4 py-8 text-center text-sm text-text-faint">Loading…</div>
              ) : results.length === 0 ? (
                <div className="px-4 py-8 text-center text-sm text-text-faint">No matches for &ldquo;{query}&rdquo;.</div>
              ) : (
                grouped.map(([section, items]) => (
                  <div key={section} className="px-2 py-1.5">
                    <div className="px-2.5 py-1 text-[11px] font-medium uppercase tracking-wide text-text-faint">
                      {section}
                    </div>
                    {items.map((item) => {
                      const idx = indexById.get(item.id) ?? 0;
                      const isActive = idx === active;
                      return (
                        <button
                          key={item.id}
                          onMouseEnter={() => setActive(idx)}
                          onClick={() => go(item)}
                          className={cx(
                            "flex w-full items-center justify-between gap-3 rounded-lg px-2.5 py-2 text-left text-[14px] transition-colors duration-100",
                            isActive ? "bg-accent-soft text-accent" : "text-text-muted hover:text-text"
                          )}
                        >
                          <span className="truncate">{item.label}</span>
                          {isActive && <CornerDownLeft className="h-3.5 w-3.5 shrink-0 opacity-60" />}
                        </button>
                      );
                    })}
                  </div>
                ))
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function groupBySection(results: SearchItem[]): [string, SearchItem[]][] {
  const map = new Map<string, SearchItem[]>();
  for (const item of results) {
    const list = map.get(item.section);
    if (list) list.push(item);
    else map.set(item.section, [item]);
  }
  return Array.from(map.entries());
}
