import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, constants } from 'node:zlib';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import type { Plugin, PluginOption } from 'vite';
import { defineConfig } from 'vitest/config';

const outDir = fileURLToPath(new URL('../internal/webui/dist', import.meta.url));
const compressible = /\.(js|css|html|svg|json|webmanifest|txt)$/;

// internal/webui serves <file>.gz with Content-Encoding: gzip when the client
// accepts it and falls back to <file>; both are embedded.
function precompress(): Plugin {
  return {
    name: 'bothy-precompress',
    apply: 'build',
    closeBundle() {
      const walk = (dir: string) => {
        for (const name of readdirSync(dir)) {
          const p = join(dir, name);
          if (statSync(p).isDirectory()) walk(p);
          else if (compressible.test(name)) {
            const gz = gzipSync(readFileSync(p), { level: constants.Z_BEST_COMPRESSION });
            writeFileSync(`${p}.gz`, gz);
          }
        }
      };
      walk(outDir);
    },
  };
}

const mock = process.env.BOTHY_MOCK === '1';
const plugins: PluginOption[] = [tailwindcss(), svelte({ configFile: false, compilerOptions: { css: 'external', discloseVersion: false } }), precompress()];
// The fixture gateway (web/mock) is loaded only with BOTHY_MOCK=1 (npm run mock, npm run preview:mock).
if (mock) plugins.push((await import('./mock/server.ts')).mockGateway());

export default defineConfig({
  plugins,
  build: {
    outDir,
    emptyOutDir: true,
    assetsDir: 'assets',
    modulePreload: { polyfill: false },
    sourcemap: false,
    target: 'es2022',
  },
  server: {
    proxy: mock
      ? undefined
      : {
          '/api': 'http://127.0.0.1:8080',
          '/v1': 'http://127.0.0.1:8080',
          '/healthz': 'http://127.0.0.1:8080',
        },
  },
  resolve: process.env.VITEST ? { conditions: ['browser'] } : undefined,
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['src/test/setup.ts'],
    restoreMocks: true,
  },
});
