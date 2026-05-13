export interface ReadOnlySqlValidationOptions {
  defaultLimit?: number;
  maxLimit?: number;
}

export interface ReadOnlySqlValidationResult {
  ok: boolean;
  sql: string;
  diagnostics: string[];
}

const DEFAULT_LIMIT = 100;
const DEFAULT_MAX_LIMIT = 1000;
const FORBIDDEN_SQL_TOKENS = [
  'attach',
  'alter',
  'create',
  'delete',
  'detach',
  'drop',
  'insert',
  'pragma',
  'reindex',
  'replace',
  'truncate',
  'update',
  'vacuum',
  'load_extension',
  'readfile',
  'writefile',
  'http_get',
  'http_post',
  'fetch',
];

export function validateReadOnlySql(sql: string, options: ReadOnlySqlValidationOptions = {}): ReadOnlySqlValidationResult {
  const normalized = stripSingleTrailingSemicolon(sql.trim());
  const diagnostics: string[] = [];
  if (!normalized) diagnostics.push('SQL is required.');

  const masked = maskSqlCommentsAndStrings(normalized);
  if (hasChainedStatements(masked)) diagnostics.push('Only one SQL statement is allowed.');
  if (!/^\s*(select|with)\b/i.test(masked)) diagnostics.push('Only SELECT or WITH SELECT statements are allowed.');

  const forbidden = firstForbiddenToken(masked);
  if (forbidden) diagnostics.push(`Forbidden SQL operation: ${forbidden}.`);

  if (diagnostics.length > 0) {
    return { ok: false, sql: '', diagnostics };
  }

  return {
    ok: true,
    sql: applyReadOnlyLimit(normalized, masked, options),
    diagnostics: [],
  };
}

export function isReadOnlySql(sql: string): boolean {
  return validateReadOnlySql(sql).ok;
}

function applyReadOnlyLimit(sql: string, masked: string, options: ReadOnlySqlValidationOptions): string {
  const defaultLimit = normalizeLimit(options.defaultLimit, DEFAULT_LIMIT);
  const maxLimit = normalizeLimit(options.maxLimit, DEFAULT_MAX_LIMIT);
  const limit = Math.min(defaultLimit, maxLimit);
  const lastLimit = lastLimitMatch(masked);
  if (!lastLimit) return `${sql} LIMIT ${limit}`;

  const requested = Number(lastLimit.value);
  if (Number.isFinite(requested) && requested > maxLimit) {
    return `${sql.slice(0, lastLimit.valueStart)}${maxLimit}${sql.slice(lastLimit.valueEnd)}`;
  }
  return sql;
}

function lastLimitMatch(masked: string): { value: string; valueStart: number; valueEnd: number } | null {
  const regex = /\blimit\s+(\d+)\b/ig;
  let found: RegExpExecArray | null = null;
  let next: RegExpExecArray | null;
  while ((next = regex.exec(masked)) !== null) found = next;
  if (!found) return null;
  const value = found[1] ?? '';
  const valueStart = found.index + found[0].lastIndexOf(value);
  return {
    value,
    valueStart,
    valueEnd: valueStart + value.length,
  };
}

function normalizeLimit(value: number | null | undefined, fallback: number): number {
  const numeric = Math.floor(Number(value));
  if (!Number.isFinite(numeric) || numeric <= 0) return fallback;
  return Math.max(1, Math.min(10_000, numeric));
}

function firstForbiddenToken(masked: string): string | null {
  for (const token of FORBIDDEN_SQL_TOKENS) {
    const pattern = new RegExp(`\\b${escapeRegExp(token)}\\b`, 'i');
    if (pattern.test(masked)) return token;
  }
  return null;
}

function hasChainedStatements(masked: string): boolean {
  const trimmed = masked.trim();
  const firstSemicolon = trimmed.indexOf(';');
  if (firstSemicolon < 0) return false;
  return trimmed.slice(firstSemicolon + 1).trim().length > 0;
}

function stripSingleTrailingSemicolon(sql: string): string {
  return sql.replace(/;\s*$/, '').trim();
}

function maskSqlCommentsAndStrings(sql: string): string {
  let output = '';
  let index = 0;
  while (index < sql.length) {
    const char = sql[index];
    const next = sql[index + 1];

    if (char === '-' && next === '-') {
      const end = sql.indexOf('\n', index + 2);
      const stop = end === -1 ? sql.length : end;
      output += ' '.repeat(stop - index);
      index = stop;
      continue;
    }

    if (char === '/' && next === '*') {
      const end = sql.indexOf('*/', index + 2);
      const stop = end === -1 ? sql.length : end + 2;
      output += ' '.repeat(stop - index);
      index = stop;
      continue;
    }

    if (char === '\'' || char === '"') {
      const quote = char;
      output += ' ';
      index += 1;
      while (index < sql.length) {
        const current = sql[index];
        output += ' ';
        if (current === quote) {
          if (sql[index + 1] === quote) {
            output += ' ';
            index += 2;
            continue;
          }
          index += 1;
          break;
        }
        index += 1;
      }
      continue;
    }

    output += char;
    index += 1;
  }
  return output;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
