export const API_URL = window.storeOpen?.apiUrl ?? "http://127.0.0.1:8766";

export type ArtistStyleOption = {
  value: string;
  label: string;
  description: string;
  prompt: string;
};

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

type ApiProxyResult = {
  ok: boolean;
  status: number;
  body: string;
  contentType: string;
  error?: string;
};

function parseErrorBody(text: string, status: number) {
  const trimmed = text.trim();
  if (!trimmed) return `Lỗi HTTP ${status}`;
  try {
    const parsed = JSON.parse(trimmed);
    if (typeof parsed.detail === "string") return parsed.detail;
    if (Array.isArray(parsed.detail)) return parsed.detail.map((d: any) => d.msg || String(d)).join("; ");
    return parsed.message || trimmed;
  } catch {
    return trimmed;
  }
}

function parseResponse<T>(body: string, contentType: string): T {
  if (contentType.includes("application/json")) {
    return JSON.parse(body) as T;
  }
  return body as T;
}

async function requestViaElectron(path: string, init?: RequestInit, retries = 3): Promise<ApiProxyResult> {
  const apiRequest = window.storeOpen?.apiRequest;
  if (!apiRequest) throw new Error("no electron api proxy");
  const method = init?.method;
  const body = typeof init?.body === "string" ? init.body : init?.body ? JSON.stringify(init.body) : undefined;
  let last: ApiProxyResult = { ok: false, status: 0, body: "", contentType: "", error: "unknown" };
  for (let attempt = 0; attempt <= retries; attempt++) {
    last = await apiRequest(path, { method, body });
    if (last.ok || (last.status >= 400 && last.status < 600)) return last;
    // #region agent log
    fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'client.ts:requestViaElectron',message:'ipc api error',data:{path,attempt,error:last.error,status:last.status},timestamp:Date.now(),hypothesisId:'ipc',runId:'backend-fix2'})}).catch(()=>{});
    // #endregion
    if (attempt < retries) await sleep(600 * (attempt + 1));
  }
  return last;
}

async function requestViaFetch(path: string, init?: RequestInit, retries = 2): Promise<Response> {
  let lastNetworkError: unknown;
  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      return await fetch(`${API_URL}${path}`, {
        headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
        ...init
      });
    } catch (err) {
      lastNetworkError = err;
      // #region agent log
      fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'client.ts:requestViaFetch',message:'fetch network error',data:{path,attempt,apiUrl:API_URL,error:String(err)},timestamp:Date.now(),hypothesisId:'fetch',runId:'backend-fix2'})}).catch(()=>{});
      // #endregion
      if (attempt < retries) {
        await sleep(500 * (attempt + 1));
        continue;
      }
      throw err;
    }
  }
  throw lastNetworkError instanceof Error ? lastNetworkError : new Error("fetch failed");
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  if (window.storeOpen?.apiRequest) {
    const res = await requestViaElectron(path, init);
    if (!res.ok) {
      if (res.status === 0) {
        throw new Error(
          `Backend tạm thời không phản hồi (${API_URL}). ` +
          `Chi tiết: ${res.error || "không kết nối được"}. ` +
          "Hãy chạy npm run dev và đợi dòng Application startup complete."
        );
      }
      throw new Error(parseErrorBody(res.body, res.status));
    }
    return parseResponse<T>(res.body, res.contentType);
  }

  const res = await requestViaFetch(path, init);
  if (!res.ok) throw new Error(parseErrorBody(await res.text(), res.status));
  const contentType = res.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) return res.json();
  return (await res.text()) as T;
}

export const api = {
  health: () => request<{ ok: boolean; api?: number }>("/health"),
  projects: () => request<any[]>("/projects"),
  createProject: (body: any) => request<any>("/projects", { method: "POST", body: JSON.stringify(body) }),
  snapshot: (id: string) => request<any>(`/projects/${id}/snapshot`),
  start: (id: string, body: any) => request<any>(`/projects/${id}/start`, { method: "POST", body: JSON.stringify(body) }),
  resume: (id: string) => request<any>(`/projects/${id}/resume`, { method: "POST" }),
  abort: (id: string) => request<any>(`/projects/${id}/abort`, { method: "POST" }),
  steer: (id: string, text: string) => request<any>(`/projects/${id}/steer`, { method: "POST", body: JSON.stringify({ text }) }),
  cont: (id: string, text: string) => request<any>(`/projects/${id}/continue`, { method: "POST", body: JSON.stringify({ text }) }),
  chapters: (id: string) => request<any[]>(`/projects/${id}/chapters`),
  chapter: (id: string, n: number) => request<any>(`/projects/${id}/chapters/${n}`),
  patchChapter: (id: string, n: number, body: any) => request<any>(`/projects/${id}/chapters/${n}`, { method: "PATCH", body: JSON.stringify(body) }),
  outline: (id: string) => request<any>(`/projects/${id}/outline`),
  characters: (id: string) => request<any>(`/projects/${id}/characters`),
  world: (id: string) => request<any>(`/projects/${id}/world`),
  reviews: (id: string) => request<any[]>(`/projects/${id}/reviews`),
  artistPrompts: (id: string) => request<any[]>(`/projects/${id}/artist-prompts`),
  regenerateArtistPrompts: (id: string, chapterNo: number, runId?: string) =>
    request<any>(`/projects/${id}/artist-prompts/regenerate`, { method: "POST", body: JSON.stringify({ chapter_no: chapterNo, run_id: runId }) }),
  diagnostics: (id: string) => request<any>(`/projects/${id}/diagnostics`),
  importNovel: (id: string, body: any) => request<any>(`/projects/${id}/import`, { method: "POST", body: JSON.stringify(body) }),
  simulate: (id: string, body: any) => request<any>(`/projects/${id}/simulate`, { method: "POST", body: JSON.stringify(body) }),
  importSimulation: (id: string, body: any) => request<any>(`/projects/${id}/simulation/import`, { method: "POST", body: JSON.stringify(body) }),
  exportProject: (id: string, body: any) => request<any>(`/projects/${id}/export`, { method: "POST", body: JSON.stringify(body) }),
  styles: () => request<any[]>("/settings/styles"),
  artistStyles: () => request<ArtistStyleOption[]>("/settings/artist-styles"),
  providerPresets: () => request<any[]>("/settings/providers/presets"),
  providers: () => request<any[]>("/settings/providers"),
  saveProvider: (body: any) => request<any>("/settings/providers", { method: "POST", body: JSON.stringify(body) }),
  models: () => request<any>("/settings/models"),
  setRoleModel: (body: any) => request<any>("/settings/models/roles", { method: "PATCH", body: JSON.stringify(body) }),
  usage: (projectId?: string) => request<any[]>(`/usage${projectId ? `?project_id=${projectId}` : ""}`),
  projectUsage: (id: string) => request<any[]>(`/projects/${id}/usage`),
  cocreate: (id: string, body: any) => request<any>(`/projects/${id}/cocreate`, { method: "POST", body: JSON.stringify(body) }),
  reopen: (id: string, body: any) => request<any>(`/projects/${id}/reopen`, { method: "POST", body: JSON.stringify(body) }),
  patchProject: (id: string, body: any) => request<any>(`/projects/${id}`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteProject: (id: string) => request<any>(`/projects/${id}`, { method: "DELETE" }),
  exportDiagnostics: (id: string) => request<string>(`/projects/${id}/diagnostics/export`, { method: "POST" }),
  patchOutline: (id: string, body: any) => request<any>(`/projects/${id}/outline`, { method: "PATCH", body: JSON.stringify(body) }),
  deleteProvider: (name: string) => request<any>(`/settings/providers/${name}`, { method: "DELETE" }),
  deleteRoleModel: (role: string) => request<any>(`/settings/models/roles/${role}`, { method: "DELETE" })
};

export function stream(projectId: string): WebSocket {
  return new WebSocket(`${API_URL.replace("http", "ws")}/projects/${projectId}/stream`);
}
