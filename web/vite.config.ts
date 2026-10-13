import { existsSync, readdirSync, readFileSync, statSync, unlinkSync, writeFileSync } from 'node:fs';
import { extname, join, normalize, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, constants } from 'node:zlib';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import type { Plugin, PluginOption } from 'vite';
import { defineConfig } from 'vitest/config';

const outDir = fileURLToPath(new URL('../internal/webui/dist', import.meta.url));
const compressible = /\.(js|css|html|svg|json|webmanifest|txt)$/;

// internal/webui embeds only <file>.gz for compressible files: it sends it
// with Content-Encoding: gzip, or gunzips it for the rare client without gzip.
// index.html stays raw too (tiny; the size gate and placeholder check read it).
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
            if (p !== join(outDir, 'index.html')) unlinkSync(p);
          }
        }
      };
      walk(outDir);
    },
  };
}

const gzTypes: Record<string, string> = {
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json',
  '.webmanifest': 'application/manifest+json',
  '.txt': 'text/plain; charset=utf-8',
};

// `vite preview` serves the build the way the gateway does: only <file>.gz is
// left for compressible files.
function previewGzip(): Plugin {
  return {
    name: 'bothy-preview-gzip',
    configurePreviewServer(server) {
      server.middlewares.use((req, res, next) => {
        let file: string;
        try {
          file = normalize(join(outDir, decodeURIComponent((req.url ?? '/').split('?')[0])));
        } catch {
          return next();
        }
        if (!file.startsWith(outDir + sep) || !compressible.test(file) || existsSync(file) || !existsSync(`${file}.gz`)) return next();
        res.setHeader('Content-Type', gzTypes[extname(file)]);
        res.setHeader('Content-Encoding', 'gzip');
        res.setHeader('Vary', 'Accept-Encoding');
        res.end(readFileSync(`${file}.gz`));
      });
    },
  };
}

const mock = process.env.BOTHY_MOCK === '1';
const plugins: PluginOption[] = [tailwindcss(), svelte({ configFile: false, compilerOptions: { css: 'external', discloseVersion: false } }), precompress()];
// The fixture gateway (web/mock) is loaded only with BOTHY_MOCK=1 (npm run mock, npm run preview:mock).
if (mock) plugins.push((await import('./mock/server.ts')).mockGateway());
plugins.push(previewGzip());

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
