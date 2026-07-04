/**
 * Loop D.2: the epoch profile SQL module was promoted into core
 * (`src/epoch-query-sql.ts`) as part of the primary public query API.
 * This shim keeps the historical webUI import path working.
 */
export * from '../../epoch-query-sql';
