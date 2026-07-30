/** NODE HEALTH — the design's `SDN Console.dc.html:863` entry. */
export const id = 'health';
export const title = 'NODE HEALTH';
export const spans = [4, 6];
export const def = 4;
export const order = 1;
/**
 * Anonymous-safe. The panel's headline (ONLINE/OFFLINE, peer id, API, GATEWAY)
 * is all public feed; the STORAGE line inside it is the one privileged cell and
 * it gates itself on `runtime.privileged`.
 */
export const privileged = false;
export const defaultSpan = 4;
/**
 * Anonymous view: 6, not 4. Dropping the two admin-only widgets out of a
 * 12-column row would leave a four-column hole, so the two survivors take the
 * width the missing ones freed — using only spans this widget already declares.
 */
export const publicSpan = 6;
