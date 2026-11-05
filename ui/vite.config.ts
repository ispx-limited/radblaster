import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  // Relative asset paths so the build works wherever the binary serves it from.
  base: "./",
  build: { outDir: "dist", emptyOutDir: true, assetsDir: "assets" },
});
