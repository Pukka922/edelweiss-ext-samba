import solid from "vite-plugin-solid";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [solid()],
  // vmThreads makes jsdom once per worker and still keeps each test file apart
  test: { environment: "jsdom", pool: "vmThreads" },
});
