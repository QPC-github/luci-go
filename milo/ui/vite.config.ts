// Copyright 2023 The LUCI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import replace from '@rollup/plugin-replace';
import react from '@vitejs/plugin-react';
import * as path from 'path';
import { defineConfig, loadEnv } from 'vite';
import { VitePWA } from 'vite-plugin-pwa';

/**
 * Get a boolean from the envDir.
 *
 * Return true/false if the value matches 'true'/'false' (case sensitive).
 * Otherwise, return null.
 */
function getBoolEnv(envDir: Record<string, string>, key: string): boolean | null {
  const value = envDir[key];
  if (value === 'true') {
    return true;
  }
  if (value === 'false') {
    return false;
  }
  return null;
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd());

  const localDevConfigs: typeof CONFIGS = {
    RESULT_DB: {
      HOST: env['VITE_RESULT_DB_HOST'],
    },
    BUILDBUCKET: {
      HOST: env['VITE_BUILDBUCKET_HOST'],
    },
    LUCI_ANALYSIS: {
      HOST: env['VITE_LUCI_ANALYSIS_HOST'],
    },
  };
  const localDevConfigsJs = `self.CONFIGS=Object.freeze(${JSON.stringify(localDevConfigs, undefined, 2)});`;

  return {
    base: '/ui',
    build: {
      outDir: 'out',
      assetsDir: 'immutable',
      sourcemap: true,
      rollupOptions: {
        input: {
          index: 'index.html',
          root_sw: './src/service_workers/root_sw.ts',
        },
        output: {
          entryFileNames: (chunkInfo) => (chunkInfo.name === 'index' ? 'immutable/[name]-[hash:8].js' : '[name].js'),
        },
      },
    },
    plugins: [
      // Use a plugin instead of the `define` property to substitute the const
      // variables so the VitePWA can use it.
      replace({
        preventAssignment: true,
        // We have different building pipeline for dev/test/production builds.
        // Limits the impact of the pipeline-specific features to only the
        // entry files for better consistently across different builds.
        include: ['./src/index.tsx', './src/service_workers/ui_sw.ts'],
        values: {
          ENABLE_UI_SW: JSON.stringify(getBoolEnv(env, 'VITE_ENABLE_UI_SW') ?? true),
          UI_SW_SKIP_WAITING: JSON.stringify(getBoolEnv(env, 'VITE_UI_SW_SKIP_WAITING') ?? false),
          ENABLE_GA: JSON.stringify(getBoolEnv(env, 'VITE_ENABLE_GA') ?? true),
        },
      }),
      {
        name: 'virtual-configs-js',
        resolveId: (id, importer) => {
          if (id !== 'virtual:configs.js') {
            return null;
          }

          // `importScripts` is only available in workers.
          // Ensure this module is only used by service workers.
          if (!importer?.startsWith(path.join(__dirname, 'src/service_workers'))) {
            throw new Error('virtual:configs.js should only be imported by a service worker script.');
          }
          return '\0virtual:config.js';
        },
        load: (id) => {
          if (id !== '\0virtual:config.js') {
            return null;
          }
          // During development, the service worker script can only be a JS
          // module, because it runs through the same pipeline as the rest of
          // the scripts. It cannot use the `importScripts`. So we inject the
          // configs directly.
          // In production, the service worker script cannot be a JS module.
          // Because that has limited browser support. So we need to use
          // `importScripts` instead of `import` to load `/configs.js`.
          return mode === 'development' ? localDevConfigsJs : "importScripts('/configs.js');";
        },
      },
      {
        name: 'inject-configs-js-in-html',
        // Vite resolves external resources with relative URLs (URLs without a
        // domain name) inconsistently.
        // Inject `<script src="/configs.js" ><script>` via a plugin to prevent
        // Vite from conditionally prepend "/ui" prefix onto the URL.
        transformIndexHtml: (html) => ({
          html,
          tags: [
            {
              tag: 'script',
              attrs: {
                src: '/configs.js',
              },
              injectTo: 'head',
            },
          ],
        }),
      },
      {
        name: 'dev-server',
        configureServer: (server) => {
          // Serve `/root_sw.js`
          server.middlewares.use((req, _res, next) => {
            if (req.url === '/root_sw.js') {
              req.url = '/ui/src/service_workers/root_sw.ts';
            }
            return next();
          });

          // Serve `/configs.js` during development.
          // We don't want to define `CONFIGS` directly because that would
          // prevent us from testing the service worker's `GET '/configs.js'`
          // handler.
          server.middlewares.use((req, res, next) => {
            if (req.url !== '/configs.js') {
              return next();
            }
            res.setHeader('content-type', 'application/javascript');
            res.end(localDevConfigsJs);
          });
        },
      },
      react({
        babel: {
          configFile: true,
        },
      }),
      VitePWA({
        injectRegister: null,
        strategies: 'injectManifest',
        srcDir: 'src/service_workers',
        filename: 'ui_sw.ts',
        outDir: 'out',
        devOptions: {
          enabled: true,
          // During development, the service worker script can only be a JS
          // module, because it runs through the same pipeline as the rest of
          // the scripts. It cannot use the `importScripts`. So we inject the
          // configs directly.
          type: 'module',
          navigateFallback: 'index.html',
        },
        injectManifest: {
          globPatterns: ['**/*.{js,css,html}'],
          vitePlugins(vitePluginIds) {
            return vitePluginIds.filter(
              (id) =>
                // Don't include the plugin itself.
                !id.startsWith('vite-plugin-pwa') &&
                // Don't need any HTML related plugins.
                !id.includes('html')
            );
          },
        },
      }),
    ],
    server: {
      port: 8080,
      strictPort: true,
      proxy: {
        '^(?!/ui/).*$': {
          target: env['VITE_MILO_URL'],
          changeOrigin: true,
          secure: false,
        },
      },
    },
  };
});
