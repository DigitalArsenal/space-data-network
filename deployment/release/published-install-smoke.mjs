#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const PUBLIC_UNIX_INSTALL_URL = 'https://spacedatanetwork.org/install.sh';
export const PUBLIC_WINDOWS_INSTALL_URL = 'https://spacedatanetwork.org/install.ps1';
export const SMOKE_COMMANDS = ['version', 'init', 'show-identity', 'status'];
export const DEFAULT_COMMAND_TIMEOUT_MS = 900_000;

function platformPath(platform, ...parts) {
  if (platform === 'windows') {
    return parts
      .map((part, index) => {
        const text = String(part);
        if (index === 0) {
          return text.replace(/[\\/]$/, '');
        }
        return text.replace(/^[\\/]/, '').replace(/[\\/]$/, '');
      })
      .filter(Boolean)
      .join('\\');
  }
  return parts
    .map((part, index) => {
      const text = String(part);
      if (index === 0) {
        return text.replace(/\/$/, '') || '/';
      }
      return text.replace(/^\//, '').replace(/\/$/, '');
    })
    .filter(Boolean)
    .join('/');
}

export function resolvePublishedInstallSmokePlatform(platform = process.platform) {
  const normalized = String(platform ?? '').toLowerCase();
  if (normalized === 'linux' || normalized === 'darwin' || normalized === 'macos' || normalized === 'unix') {
    return 'unix';
  }
  if (normalized === 'win32' || normalized === 'windows') {
    return 'windows';
  }
  throw new Error(`Unsupported published installer smoke platform: ${platform}`);
}

function buildSmokeEnv(platform, homeDir) {
  const installDir = platformPath(homeDir === '/' ? 'unix' : platform, homeDir, '.spacedatanetwork', 'bin');
  const bundleDir = platformPath(homeDir === '/' ? 'unix' : platform, homeDir, '.spacedatanetwork', 'bundles');
  if (platform === 'windows') {
    return {
      USERPROFILE: homeDir,
      SDN_INSTALL_DIR: installDir,
      SDN_BUNDLE_DIR: bundleDir
    };
  }
  return {
    HOME: homeDir,
    SDN_INSTALL_DIR: installDir,
    SDN_BUNDLE_DIR: bundleDir
  };
}

export function buildPublishedInstallSmokePlan({
  platform = resolvePublishedInstallSmokePlatform(),
  homeDir,
  unixInstallUrl = PUBLIC_UNIX_INSTALL_URL,
  windowsInstallUrl = PUBLIC_WINDOWS_INSTALL_URL
} = {}) {
  const normalizedPlatform = resolvePublishedInstallSmokePlatform(platform);
  if (!homeDir) {
    throw new Error('homeDir is required');
  }

  const env = buildSmokeEnv(normalizedPlatform, homeDir);
  const installDir = env.SDN_INSTALL_DIR;
  const cliPath = normalizedPlatform === 'windows'
    ? platformPath('windows', installDir, 'spacedatanetwork.cmd')
    : platformPath('unix', installDir, 'spacedatanetwork');

  const install = normalizedPlatform === 'windows'
    ? {
        command: 'powershell.exe',
        args: ['-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command', `irm ${windowsInstallUrl} | iex`],
        shell: false
      }
    : {
        command: 'bash',
        args: ['-lc', `curl -fsSL ${unixInstallUrl} | bash`],
        shell: false
      };

  return {
    platform: normalizedPlatform,
    homeDir,
    env,
    install,
    commands: SMOKE_COMMANDS.map((command) => ({
      command: cliPath,
      args: command.split(' '),
      shell: normalizedPlatform === 'windows'
    }))
  };
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    if (key === '--keep-home') {
      options.keepHome = true;
      continue;
    }
    if (!key.startsWith('--')) {
      throw new Error(`Unexpected argument: ${key}`);
    }
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`Missing value for ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

function mergedEnv(plan) {
  const env = {
    ...process.env,
    ...plan.env
  };
  env.PATH = `${plan.env.SDN_INSTALL_DIR}${delimiter}${process.env.PATH ?? ''}`;
  if (process.platform === 'win32') {
    env.Path = `${plan.env.SDN_INSTALL_DIR}${delimiter}${process.env.Path ?? process.env.PATH ?? ''}`;
  }
  return env;
}

function runCommand(label, command, args, options) {
  console.log(`[published-install-smoke] ${label}: ${command} ${args.join(' ')}`);
  const result = spawnSync(command, args, {
    env: options.env,
    shell: options.shell,
    stdio: 'inherit',
    timeout: options.timeoutMs
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`${label} failed with exit code ${result.status}`);
  }
}

export function runPublishedInstallSmoke({
  platform = resolvePublishedInstallSmokePlatform(),
  homeDir,
  keepHome = false,
  timeoutMs = DEFAULT_COMMAND_TIMEOUT_MS,
  unixInstallUrl = PUBLIC_UNIX_INSTALL_URL,
  windowsInstallUrl = PUBLIC_WINDOWS_INSTALL_URL
} = {}) {
  const normalizedPlatform = resolvePublishedInstallSmokePlatform(platform);
  const generatedHome = !homeDir;
  const smokeHome = homeDir ?? mkdtempSync(join(tmpdir(), 'sdn-published-install-'));
  const plan = buildPublishedInstallSmokePlan({
    platform: normalizedPlatform,
    homeDir: smokeHome,
    unixInstallUrl,
    windowsInstallUrl
  });
  const env = mergedEnv(plan);

  try {
    runCommand('install', plan.install.command, plan.install.args, {
      env,
      shell: plan.install.shell,
      timeoutMs
    });
    for (const command of plan.commands) {
      runCommand(command.args.join(' '), command.command, command.args, {
        env,
        shell: command.shell,
        timeoutMs
      });
    }
  } finally {
    if (generatedHome && !keepHome) {
      rmSync(smokeHome, { recursive: true, force: true });
    } else {
      console.log(`[published-install-smoke] preserved smoke home: ${smokeHome}`);
    }
  }
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  runPublishedInstallSmoke({
    platform: options.platform,
    homeDir: options.homeDir,
    keepHome: options.keepHome,
    timeoutMs: options.timeoutMs ? Number.parseInt(options.timeoutMs, 10) : DEFAULT_COMMAND_TIMEOUT_MS,
    unixInstallUrl: options.unixInstallUrl,
    windowsInstallUrl: options.windowsInstallUrl
  });
}

if (resolve(process.argv[1] ?? '') === fileURLToPath(import.meta.url)) {
  main();
}
