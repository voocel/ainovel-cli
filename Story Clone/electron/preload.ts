import { contextBridge, ipcRenderer } from "electron";

contextBridge.exposeInMainWorld("storeOpen", {
  apiUrl: "http://127.0.0.1:8766",
  apiRequest: (path: string, init?: { method?: string; body?: string; timeoutMs?: number }) =>
    ipcRenderer.invoke("api-request", { path, init }),
  pickFile: () => ipcRenderer.invoke("pick-file"),
  pickFolder: () => ipcRenderer.invoke("pick-folder"),
  saveFile: (defaultName: string, ext: string) => ipcRenderer.invoke("save-file", defaultName, ext),
  notify: (title: string, body: string) => ipcRenderer.invoke("notify", title, body),
  restartBackend: () => ipcRenderer.invoke("restart-backend") as Promise<{ ok: boolean; api: number }>,
  // License API (dùng cho renderer chính nếu cần kiểm tra)
  checkLicense: () => ipcRenderer.invoke("check-license"),
});
