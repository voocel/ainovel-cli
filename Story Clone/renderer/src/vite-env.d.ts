/// <reference types="vite/client" />

declare global {
  interface Window {
    storeOpen?: {
      apiUrl: string;
      apiRequest?: (path: string, init?: { method?: string; body?: string; timeoutMs?: number }) => Promise<{
        ok: boolean;
        status: number;
        body: string;
        contentType: string;
        error?: string;
      }>;
      pickFile: () => Promise<string | null>;
      pickFolder: () => Promise<string | null>;
      saveFile: (defaultName: string, ext: string) => Promise<string | null>;
      notify: (title: string, body: string) => Promise<void>;
      restartBackend?: () => Promise<{ ok: boolean; api: number }>;
    };
  }
}

export {};
