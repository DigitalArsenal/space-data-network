import { validateReadOnlySql } from './read-only-sql-sandbox';
import type { LocalLlmQueryContext, LocalLlmQueryDraft, LocalLlmQueryRequest } from './llm-query-context';
import { findSemanticCountryMention } from './semantic-datasets';

export interface LocalLlmQueryAdapter {
  onContext?: (context: LocalLlmQueryContext) => void;
  draftSql(request: LocalLlmQueryRequest): Promise<LocalLlmQueryDraft>;
}

export function createDeterministicLocalLlmQueryAdapter(): LocalLlmQueryAdapter {
  return {
    async draftSql(request) {
      this.onContext?.(sanitizeAdapterContext(request.context));
      const sql = deterministicSqlForAsk(request.ask, request.context);
      const validation = validateReadOnlySql(sql, {
        defaultLimit: request.context.limits.maxRows,
        maxLimit: request.context.limits.maxRows,
      });
      if (!validation.ok) {
        throw new Error(`Generated SQL failed local read-only validation: ${validation.diagnostics.join(' ')}`);
      }
      return {
        sql: validation.sql,
        rationale: 'Drafted from local schema, local sample rows, and local semantic datasets only.',
      };
    },
  };
}

function deterministicSqlForAsk(ask: string, context: LocalLlmQueryContext): string {
  const tableName = context.schema.tableName || context.schema.standardId;
  const lowerAsk = ask.toLowerCase();
  if (
    context.schema.standardId === 'OMM' &&
    lowerAsk.includes('former soviet') &&
    (lowerAsk.includes('period') || lowerAsk.includes('greater than 1 day'))
  ) {
    if (context.schema.columns.includes('COUNTRY') && context.schema.columns.includes('PERIOD')) {
      return `SELECT * FROM ${tableName} WHERE COUNTRY IN ('Russia', 'Ukraine', 'Kazakhstan', 'Belarus') AND PERIOD > 1`;
    }
    if (context.schema.columns.includes('MEAN_MOTION')) {
      return `SELECT * FROM ${tableName} WHERE MEAN_MOTION < 1`;
    }
  }

  const country = findSemanticCountryMention(ask);
  if (country) {
    const columns = new Set(context.schema.columns.map((column) => column.toUpperCase()));
    if (
      context.schema.standardId === 'CAT' &&
      columns.has('OWNER') &&
      typeof country.value === 'number' &&
      Number.isInteger(country.value)
    ) {
      // CAT.OWNER is the pinned SDS legacyCountryCode byte enum. FlatSQL stores
      // it as its numeric ordinal, so string country literals cannot match it.
      return `SELECT * FROM ${tableName} WHERE OWNER = ${country.value}`;
    }
    if (columns.has('COUNTRY')) {
      // Compatibility for datasets that genuinely expose a string COUNTRY
      // column. The literal comes from pinned metadata, never from the ask.
      return `SELECT * FROM ${tableName} WHERE COUNTRY = ${sqlStringLiteral(country.country)}`;
    }
  }
  return `SELECT * FROM ${tableName}`;
}

function sqlStringLiteral(value: string): string {
  return `'${value.replaceAll("'", "''")}'`;
}

function sanitizeAdapterContext(context: LocalLlmQueryContext): LocalLlmQueryContext {
  return {
    ...context,
    sampleRows: context.sampleRows.map((row) => {
      const out: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(row)) {
        if (value instanceof Uint8Array || value instanceof ArrayBuffer) continue;
        if (/bytes|base64|private|secret|signature/i.test(key)) continue;
        out[key] = value;
      }
      return out;
    }),
  };
}
