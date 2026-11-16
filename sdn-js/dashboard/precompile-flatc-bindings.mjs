// Capture the pinned compiler's fixed Embind factories during initialization.
// The dashboard build emits these as ordinary functions for its strict CSP.
import { pathToFileURL } from 'node:url';

const { default: createFlatcModule } = await import(pathToFileURL(process.argv[2]).href);
const originalFunction = globalThis.Function;
const factories = new Map();
const capture = args => {
  const parts = args.map(String);
  factories.set(JSON.stringify(parts), parts);
};
globalThis.Function = new Proxy(originalFunction, {
  apply(target, receiver, args) {
    capture(args);
    return Reflect.apply(target, receiver, args);
  },
  construct(target, args) {
    capture(args);
    return Reflect.construct(target, args);
  },
});
try {
  await createFlatcModule({ noInitialRun: true, noExitRuntime: true, print() {}, printErr() {} });
} finally {
  globalThis.Function = originalFunction;
}
if (factories.size === 0) throw new Error('The compiler produced no Embind factories');
process.stdout.write(JSON.stringify([...factories.values()]));
