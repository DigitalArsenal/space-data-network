/** SERVICE — the design's `SDN Console.dc.html:865` entry. */
export const id = 'service';
export const title = 'SERVICE';
export const spans = [4, 6];
export const def = 4;
export const order = 3;
/**
 * Admin-gated (nst-node-admin-contract item 5: "anonymous/public view
 * unchanged"). started_at, the systemd UnitFileState and the service state all
 * come from the Admin snapshot; there is no anonymous source for them.
 */
export const privileged = true;
export const defaultSpan = 4;
/* No publicSpan: absent from the anonymous layout by construction. */
