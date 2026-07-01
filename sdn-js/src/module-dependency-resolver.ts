/**
 * Module dependency resolver + installer — the browser/Helia mirror of the Go
 * sdn-server `internal/deps` package (WS4.2 / WS4.4a / WS4.4b).
 *
 * Given a root module's declared PLG dependencies and a catalog that resolves a
 * dependency to a concrete manifest, it computes the transitive install closure
 * (dependency-first, topological) and drives installation so that installing A
 * recursively pulls and registers everything A needs — the decentralized
 * package manager, browser side. The same algorithm runs in the Go/Kubo node;
 * the concrete fetch/decrypt/register wiring is supplied by the browser runtime
 * (via createModuleInstaller) reusing requestEncryptedModuleBundle.
 */

export interface ModuleDependency {
  pluginId: string;
  /** Inclusive lower bound (semver); undefined/empty = unbounded below. */
  minVersion?: string;
  /** Inclusive upper bound (semver); undefined/empty = unbounded above. */
  maxVersion?: string;
}

export interface ModuleManifest {
  pluginId: string;
  version: string;
  dependencies?: ModuleDependency[];
}

export interface ModuleCatalog {
  /** Resolve a dependency to the best published manifest satisfying its range. */
  resolve(dep: ModuleDependency): Promise<ModuleManifest>;
}

export interface InstalledModules {
  /** Installed version of pluginId, or undefined if absent. */
  installedVersion(pluginId: string): string | undefined;
}

export interface PlanStep {
  pluginId: string;
  version: string;
}

export type InstallFn = (step: PlanStep) => Promise<void>;

export type DependencyErrorCode =
  | 'cycle'
  | 'not_found'
  | 'no_satisfying_version'
  | 'version_conflict'
  | 'invalid_version';

export class DependencyError extends Error {
  readonly code: DependencyErrorCode;
  constructor(code: DependencyErrorCode, message: string) {
    super(message);
    this.name = 'DependencyError';
    this.code = code;
  }
}

// --- semver (minimal core comparison over x.y.z; matches the Go resolver's
// intent for inclusive [min,max] bounds; pre-release/build metadata ignored) ---

function parseSemverCore(v: string | undefined): [number, number, number] | null {
  const trimmed = String(v ?? '')
    .trim()
    .replace(/^v/, '');
  if (trimmed === '') return null;
  const core = trimmed.split(/[-+]/, 1)[0];
  const parts = core.split('.');
  if (parts.length === 0 || parts.length > 3) return null;
  const out: [number, number, number] = [0, 0, 0];
  for (let i = 0; i < parts.length; i++) {
    if (!/^\d+$/.test(parts[i])) return null;
    out[i] = Number(parts[i]);
  }
  return out;
}

function isValidSemver(v: string | undefined): boolean {
  return parseSemverCore(v) !== null;
}

/** compareSemver returns -1/0/1; throws DependencyError('invalid_version') on bad input. */
export function compareSemver(a: string, b: string): number {
  const pa = parseSemverCore(a);
  const pb = parseSemverCore(b);
  if (pa === null) throw new DependencyError('invalid_version', `invalid semver: ${a}`);
  if (pb === null) throw new DependencyError('invalid_version', `invalid semver: ${b}`);
  for (let i = 0; i < 3; i++) {
    if (pa[i] !== pb[i]) return pa[i] < pb[i] ? -1 : 1;
  }
  return 0;
}

/** satisfies reports whether version is within dep's inclusive [min,max]. Fails closed. */
export function satisfies(version: string, dep: ModuleDependency): boolean {
  if (parseSemverCore(version) === null) return false;
  if (dep.minVersion) {
    if (!isValidSemver(dep.minVersion) || compareSemver(version, dep.minVersion) < 0) return false;
  }
  if (dep.maxVersion) {
    if (!isValidSemver(dep.maxVersion) || compareSemver(version, dep.maxVersion) > 0) return false;
  }
  return true;
}

function validateRange(dep: ModuleDependency): void {
  if (dep.minVersion && !isValidSemver(dep.minVersion)) {
    throw new DependencyError('invalid_version', `min ${dep.minVersion}`);
  }
  if (dep.maxVersion && !isValidSemver(dep.maxVersion)) {
    throw new DependencyError('invalid_version', `max ${dep.maxVersion}`);
  }
  if (dep.minVersion && dep.maxVersion && compareSemver(dep.minVersion, dep.maxVersion) > 0) {
    throw new DependencyError('invalid_version', `min ${dep.minVersion} > max ${dep.maxVersion}`);
  }
}

function rangeLabel(dep: ModuleDependency): string {
  return `[${dep.minVersion || '*'},${dep.maxVersion || '*'}]`;
}

/**
 * resolveClosure walks root's transitive dependency graph and returns the
 * modules that must be installed, ordered so every dependency precedes its
 * dependents. Already-installed modules that satisfy are skipped (subtree
 * trusted). The root itself is never included. Throws DependencyError on a
 * cycle, missing/unsatisfiable dependency, incompatible diamond, or bad range.
 */
export async function resolveClosure(
  root: ModuleManifest,
  installed: InstalledModules | null,
  catalog: ModuleCatalog,
): Promise<PlanStep[]> {
  const chosen = new Map<string, string>();
  const inStack = new Set<string>();
  const plan: PlanStep[] = [];

  const rootId = (root.pluginId ?? '').trim();
  if (rootId) inStack.add(rootId);

  const visitDeps = async (manifest: ModuleManifest): Promise<void> => {
    const deps = [...(manifest.dependencies ?? [])].sort((a, b) =>
      (a.pluginId ?? '').localeCompare(b.pluginId ?? ''),
    );
    for (const dep of deps) {
      const id = (dep.pluginId ?? '').trim();
      if (!id) continue;
      validateRange(dep);

      const installedVer = installed?.installedVersion(id);
      if (installedVer !== undefined) {
        if (satisfies(installedVer, dep)) continue;
        throw new DependencyError(
          'version_conflict',
          `${id} installed ${installedVer} does not satisfy ${rangeLabel(dep)}`,
        );
      }

      const chosenVer = chosen.get(id);
      if (chosenVer !== undefined) {
        if (satisfies(chosenVer, dep)) continue;
        throw new DependencyError(
          'version_conflict',
          `${id} chosen ${chosenVer} does not satisfy ${rangeLabel(dep)}`,
        );
      }

      if (inStack.has(id)) {
        throw new DependencyError('cycle', `dependency cycle at ${id}`);
      }

      const depManifest = await catalog.resolve(dep);
      const resolvedId = (depManifest.pluginId ?? '').trim() || id;
      if (!satisfies(depManifest.version, dep)) {
        throw new DependencyError(
          'no_satisfying_version',
          `${id} resolved to ${depManifest.version} outside ${rangeLabel(dep)}`,
        );
      }

      inStack.add(id);
      await visitDeps({ ...depManifest, pluginId: resolvedId });
      inStack.delete(id);

      chosen.set(id, depManifest.version);
      plan.push({ pluginId: id, version: depManifest.version });
    }
  };

  await visitDeps(root);
  return plan;
}

/**
 * installClosure resolves root's closure and installs every not-yet-installed
 * module dependency-first, then the root last (unless already installed).
 * installFn is invoked once per module actually installed. Returns the ordered
 * list of installed modules; rejects (after installing prior steps) on the first
 * installFn failure.
 */
export async function installClosure(
  root: ModuleManifest,
  installed: InstalledModules | null,
  catalog: ModuleCatalog,
  installFn: InstallFn,
): Promise<PlanStep[]> {
  const closure = await resolveClosure(root, installed, catalog);
  const steps = [...closure];

  const rootId = (root.pluginId ?? '').trim();
  if (rootId && installed?.installedVersion(rootId) === undefined) {
    steps.push({ pluginId: rootId, version: root.version });
  }

  const done: PlanStep[] = [];
  for (const step of steps) {
    await installFn(step);
    done.push(step);
  }
  return done;
}

// --- in-memory catalog / installed set (mirror of Go MapCatalog/MapInstalled) ---

export class MapModuleCatalog implements ModuleCatalog {
  private readonly byId = new Map<string, ModuleManifest[]>();

  constructor(manifests: ModuleManifest[] = []) {
    for (const m of manifests) this.add(m);
  }

  add(m: ModuleManifest): void {
    const id = (m.pluginId ?? '').trim();
    if (!id) return;
    const list = this.byId.get(id) ?? [];
    list.push(m);
    this.byId.set(id, list);
  }

  async resolve(dep: ModuleDependency): Promise<ModuleManifest> {
    const id = (dep.pluginId ?? '').trim();
    const candidates = this.byId.get(id) ?? [];
    if (candidates.length === 0) {
      throw new DependencyError('not_found', `dependency not found: ${id}`);
    }
    let best: ModuleManifest | undefined;
    for (const m of candidates) {
      if (!satisfies(m.version, dep)) continue;
      if (!best || compareSemver(m.version, best.version) > 0) best = m;
    }
    if (!best) {
      throw new DependencyError('no_satisfying_version', `no satisfying version: ${id} ${rangeLabel(dep)}`);
    }
    return best;
  }
}
