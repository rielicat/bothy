import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, constants } from 'node:zlib';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, type Plugin } from 'vite';

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

export default defineConfig({
  plugins: [tailwindcss(), svelte({ configFile: false, compilerOptions: { css: 'external' } }), precompress()],
  build: {
    outDir,
    emptyOutDir: true,
    assetsDir: 'assets',
    modulePreload: { polyfill: false },
    sourcemap: false,
    target: 'es2022',
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/v1': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
});
