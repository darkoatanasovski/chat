// Renders a RequestSpec as copy-pasteable code. cURL / fetch / Python are
// generated purely from the spec (method, path, query, body) so every
// feature in lib/playground/features.ts gets all three for free; only the
// Node.js SDK snippet is hand-written per feature (Feature.sdk), since the
// SDK's method names don't map mechanically onto REST paths.
import type { HttpSpec, WsSpec } from "./client";

export type SnippetLang = "sdk" | "fetch" | "curl" | "python";

export const SNIPPET_LANGS: { id: SnippetLang; label: string }[] = [
  { id: "sdk", label: "SDK" },
  { id: "fetch", label: "fetch" },
  { id: "curl", label: "cURL" },
  { id: "python", label: "Python" },
];

/** Placeholder used everywhere instead of the actor's real token — a
 * snippet is meant to be pasted into the user's own code, where the token
 * comes from their backend's POST /users call, never from the console. */
export const TOKEN_PLACEHOLDER = "USER_TOKEN";

function queryString(spec: HttpSpec): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(spec.query ?? {})) {
    if (v !== undefined && v !== "") params.set(k, String(v));
  }
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

function toPython(value: unknown, indent = 0): string {
  const pad = "  ".repeat(indent);
  const padIn = "  ".repeat(indent + 1);
  if (value === null || value === undefined) return "None";
  if (value === true) return "True";
  if (value === false) return "False";
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return JSON.stringify(value);
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    return `[\n${value.map((v) => `${padIn}${toPython(v, indent + 1)}`).join(",\n")}\n${pad}]`;
  }
  const entries = Object.entries(value as Record<string, unknown>).filter(([, v]) => v !== undefined);
  if (entries.length === 0) return "{}";
  return `{\n${entries.map(([k, v]) => `${padIn}${JSON.stringify(k)}: ${toPython(v, indent + 1)}`).join(",\n")}\n${pad}}`;
}

export function httpSnippet(lang: Exclude<SnippetLang, "sdk">, apiBase: string, spec: HttpSpec): string {
  const url = `${apiBase}${spec.path}${queryString(spec)}`;
  const hasBody = spec.body !== undefined;

  switch (lang) {
    case "curl": {
      const lines = [`curl -X ${spec.method} "${url}" \\`, `  -H "Authorization: Bearer $${TOKEN_PLACEHOLDER}"`];
      if (hasBody) {
        lines[lines.length - 1] += " \\";
        lines.push(`  -H "Content-Type: application/json" \\`);
        lines.push(`  -d '${JSON.stringify(spec.body).replace(/'/g, "'\\''")}'`);
      }
      return lines.join("\n");
    }
    case "fetch": {
      const headers = [`Authorization: \`Bearer \${${TOKEN_PLACEHOLDER}}\``];
      if (hasBody) headers.push(`"Content-Type": "application/json"`);
      const opts = [`  method: "${spec.method}",`, `  headers: { ${headers.join(", ")} },`];
      if (hasBody) opts.push(`  body: JSON.stringify(${JSON.stringify(spec.body, null, 2).replace(/\n/g, "\n  ")}),`);
      return `const res = await fetch("${url}", {\n${opts.join("\n")}\n});\nif (!res.ok) throw new Error((await res.json()).error);\nconst data = ${spec.method === "DELETE" ? "res.status === 204 ? null : await res.json()" : "await res.json()"};`;
    }
    case "python": {
      const method = spec.method.toLowerCase();
      const args = [`"${url}"`, `headers={"Authorization": f"Bearer {${TOKEN_PLACEHOLDER}}"}`];
      if (hasBody) args.push(`json=${toPython(spec.body)}`);
      return `import requests\n\nres = requests.${method}(\n${args.map((a) => `    ${a.replace(/\n/g, "\n    ")}`).join(",\n")},\n)\nres.raise_for_status()\ndata = res.json() if res.content else None`;
    }
  }
}

export function wsSnippet(lang: Exclude<SnippetLang, "sdk">, wsBase: string, spec: WsSpec): string {
  const frame = JSON.stringify(spec.frame);
  switch (lang) {
    case "fetch":
      return `const ws = new WebSocket(\`${wsBase}/connect?token=\${${TOKEN_PLACEHOLDER}}\`);\nws.onopen = () => ws.send(${JSON.stringify(frame)});\nws.onmessage = (ev) => console.log(JSON.parse(ev.data));`;
    case "python":
      return `import json, websockets  # pip install websockets\n\nasync with websockets.connect(f"${wsBase}/connect?token={${TOKEN_PLACEHOLDER}}") as ws:\n    await ws.send(json.dumps(${toPython(spec.frame)}))\n    async for raw in ws:\n        print(json.loads(raw))`;
    case "curl":
      return `# cURL can't hold a WebSocket open — use websocat instead:\nwebsocat "${wsBase}/connect?token=$${TOKEN_PLACEHOLDER}"\n# then paste this frame and press enter:\n${frame}`;
  }
}

/** Boilerplate the per-feature SDK snippets share. */
export function sdkPrelude(apiBase: string): string {
  return `import { createChatClient } from "@chat-platform/sdk/client";\n\nconst chat = createChatClient({ baseUrl: "${apiBase}", token: ${TOKEN_PLACEHOLDER} });\n\n`;
}

export function sdkRealtimePrelude(wsBase: string): string {
  return `import { RealtimeConnection } from "@chat-platform/sdk/realtime";\n\nconst realtime = new RealtimeConnection({\n  baseUrl: "${wsBase}",\n  token: ${TOKEN_PLACEHOLDER},\n  onEvent: (event) => console.log(event.type, event),\n});\n\n`;
}
