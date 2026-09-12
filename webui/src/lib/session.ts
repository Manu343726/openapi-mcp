// Browser session plumbing: per-tab session token (server "X-Ui-Session"
// header) and the /ui/manifest + /ui/chat bridge clients that do not go
// through CopilotKit.

export const SESSION_KEY = "openapi-mcp.session";
export const SESSION_HEADER = "X-Ui-Session";

export function getSessionToken(): string {
  try {
    let tok = window.sessionStorage.getItem(SESSION_KEY);
    if (!tok) {
      tok = crypto.randomUUID();
      window.sessionStorage.setItem(SESSION_KEY, tok);
    }
    return tok;
  } catch {
    return crypto.randomUUID();
  }
}

export interface ManifestApi {
  name: string;
  targets: string[];
  active: string;
  tools: number;
  exposed: number;
  mode: string;
  session_override: boolean;
  knowledge?: { enabled?: boolean; language?: string };
}

export interface ViewPayload {
  tool?: string;
  kind?: string;
  error?: boolean;
  text?: string;
  status_code?: number;
  layout?: string;
  columns?: string[];
  rows?: Record<string, unknown>[];
  count?: number;
  view?: Record<string, unknown>;
}

export interface Manifest {
  session: string;
  apis: ManifestApi[];
  meta: { knowledge?: { enabled?: boolean } };
  scripts: { name: string; api: string; summary: string; exposed: boolean }[];
  tools: string[];
}

export async function fetchManifest(token: string): Promise<Manifest> {
  const res = await fetch("/ui/manifest", { headers: { [SESSION_HEADER]: token } });
  if (!res.ok) throw new Error(`manifest ${res.status}`);
  return (await res.json()) as Manifest;
}

export interface BridgeResult {
  text?: string;
  view?: ViewPayload;
  ok?: boolean;
  error?: string;
}

export async function postBridge(
  token: string,
  tool: string,
  args: Record<string, unknown>,
): Promise<BridgeResult> {
  const res = await fetch("/ui/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json", [SESSION_HEADER]: token },
    body: JSON.stringify({ tool, arguments: args }),
  });
  const j = (await res.json().catch(() => ({}))) as BridgeResult;
  return j;
}