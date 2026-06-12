/**
 * Configuration for the SDN MCP server.
 *
 * Values come from environment variables (and CLI flags in cli.ts):
 *   SDN_API_URL   — base URL of the SDN node admin/API server.
 *                   Defaults to http://127.0.0.1:5001 (the sdn-server default
 *                   admin listen address, see sdn-server/internal/config).
 *   SDN_API_TOKEN — value of the `sdn_wallet_session` session cookie issued by
 *                   the node's wallet challenge/response login. Only required
 *                   for sdn_publish_record on nodes with admin.require_auth
 *                   enabled; all query tools work unauthenticated.
 */
export interface SdnConfig {
  baseUrl: string;
  token?: string;
}

export const DEFAULT_SDN_API_URL = "http://127.0.0.1:5001";

export function loadConfig(env: NodeJS.ProcessEnv = process.env): SdnConfig {
  const baseUrl = (env.SDN_API_URL ?? DEFAULT_SDN_API_URL).replace(/\/+$/, "");
  const token = env.SDN_API_TOKEN?.trim() || undefined;
  return { baseUrl, token };
}
