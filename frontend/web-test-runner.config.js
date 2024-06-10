// import { esbuildPlugin } from '@web/dev-server-esbuild';

// export default {
//   files: ['src/**/*.test.ts', 'src/**/*.spec.ts'],
//   plugins: [esbuildPlugin({ ts: true })],
// };

import { vitePlugin } from '@remcovaes/web-test-runner-vite-plugin';

export default {
  files: 'src/**/*.test.ts',
  plugins: [
    vitePlugin(),
  ],
};