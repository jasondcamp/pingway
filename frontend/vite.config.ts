import { writeFileSync } from "node:fs";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [
    {
      // dist/.gitkeep keeps the go:embed directive satisfied on fresh
      // clones; recreate it after each build since emptyOutDir wipes it
      name: "keep-gitkeep",
      closeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // single-page app; /kiosk and /settings are client-side routes served
    // index.html by the Go server
  },
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
