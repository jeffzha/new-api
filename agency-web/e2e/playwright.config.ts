import { defineConfig } from '@playwright/test';
import path from 'node:path';

const testRoot = process.env.AGENCY_BROWSER_TEST_ROOT || 'E:\\new-api-test-cache\\agency-browser';

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 45000,
  expect: { timeout: 10000 },
  outputDir: path.join(testRoot, 'results'),
  reporter: [['list'], ['json', { outputFile: path.join(testRoot, 'results.json') }]],
  use: {
    actionTimeout: 10000,
    baseURL: process.env.AGENCY_BROWSER_URL || 'http://127.0.0.1:4328',
    locale: 'en-US',
    viewport: { width: 1440, height: 1000 },
    launchOptions: {
      executablePath: process.env.AGENCY_BROWSER_EXECUTABLE || 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
});
