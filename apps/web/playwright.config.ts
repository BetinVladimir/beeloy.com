import { defineConfig, devices } from "@playwright/test";

// Needs the local stack: docker compose (…compose.dev.yml) for postgres/redis/api/mailpit/s3. `pnpm dev` is started here.
export default defineConfig({
  testDir: "./e2e",
  timeout: 90_000,
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: { baseURL: "http://localhost:3000", trace: "retain-on-failure" },
  webServer: {
    command: "pnpm dev",
    url: "http://localhost:3000/en",
    reuseExistingServer: true,
    timeout: 120_000,
    env: { API_ORIGIN: "http://localhost:8080", PROXY_SHARED_SECRET: "dev-proxy-secret-dev-proxy-secret-00", NEXT_PUBLIC_SITE_URL: "http://localhost:3000", NEXT_PUBLIC_SOCIAL_PROVIDERS: "google,facebook" },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
