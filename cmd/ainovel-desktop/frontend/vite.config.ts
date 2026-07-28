import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Wails 期望前端产物输出到 frontend/dist（main.go 用 //go:embed all:frontend/dist 打包）。
export default defineConfig({
  plugins: [
    react(),
    {
      name: "keep-go-embed-target",
      generateBundle() {
        // 干净 clone 尚未构建前，Go 的 //go:embed 也要求 dist 至少有一个文件。
        // Vite 每次会清空 dist，因此由构建本身重新产出这个占位文件。
        this.emitFile({ type: "asset", fileName: ".gitkeep", source: "" });
      },
    },
  ],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
