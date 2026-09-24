import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development Vite serves the app and forwards API, WebSocket, and image
// requests to yormd. Run yormd with YORM_ALLOWED_ORIGINS=localhost:5173.
const backend = "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  // Konva is most of the bundle, and one chunk is fine for a single-page table.
  build: { chunkSizeWarningLimit: 700 },
  server: {
    proxy: {
      "/api": backend,
      "/uploads": backend,
      "/ws": { target: backend, ws: true },
    },
  },
});
