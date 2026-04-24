import * as reduxBundler from '../../../webui/node_modules/redux-bundler/dist/redux-bundler.js';

export * from '../../../webui/node_modules/redux-bundler/dist/redux-bundler.js';

const fallbackTimer = (fn) => globalThis.setTimeout(fn, 0);

export const raf = (fn) => {
  const requestAnimationFrame = globalThis.requestAnimationFrame;
  return typeof requestAnimationFrame === 'function'
    ? requestAnimationFrame.call(globalThis, fn)
    : fallbackTimer(fn);
};

export const ric = (fn, options) => {
  const requestIdleCallback = globalThis.requestIdleCallback;
  return typeof requestIdleCallback === 'function'
    ? requestIdleCallback.call(globalThis, fn, options)
    : fallbackTimer(fn);
};

export default {
  ...reduxBundler,
  raf,
  ric,
};
