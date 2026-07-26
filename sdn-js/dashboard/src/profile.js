/*
 * THIS NODE profile edit model (graph task nst-node-edit-permissions-ui
 * deliverable 2; wire contract nst-node-admin-contract §6).
 *
 * ABSOLUTE RULE — contact data is NEVER invented. Every field starts from
 * the CURRENT value the node reports at GET /api/node/epm/json and from
 * nothing else: no placeholder, no example, no value copied out of the
 * status feed, no value inferred from the vCard. A field the node does not
 * report starts EMPTY and stays empty.
 *
 * Second rule, learned from the wire: PUT /api/node/epm REPLACES the whole
 * epm.Profile (handleNodeEPM decodes a fresh struct and calls
 * Service.UpdateProfile with it). So the body must carry EVERY field —
 * including the ones this form does not edit (photo_data_url) — or saving
 * a name would silently delete a photo. `photo_data_url` is therefore
 * round-tripped verbatim and only ever cleared by an explicit operator
 * action.
 */

/** Scalar profile fields, in the order the form renders them. */
export const PROFILE_FIELDS = [
  { key: 'dn', label: 'DISPLAY NAME', hint: 'The name this node publishes (vCard FN).' },
  { key: 'legal_name', label: 'LEGAL NAME' },
  { key: 'honorific_prefix', label: 'HONORIFIC PREFIX' },
  { key: 'given_name', label: 'GIVEN NAME' },
  { key: 'additional_name', label: 'ADDITIONAL NAME' },
  { key: 'family_name', label: 'FAMILY NAME' },
  { key: 'honorific_suffix', label: 'HONORIFIC SUFFIX' },
  { key: 'job_title', label: 'JOB TITLE' },
  { key: 'occupation', label: 'OCCUPATION' },
  { key: 'email', label: 'EMAIL', type: 'email' },
  { key: 'telephone', label: 'TELEPHONE', type: 'tel' },
];

/** Address sub-object fields (epm.Address). */
export const ADDRESS_FIELDS = [
  { key: 'street', label: 'STREET' },
  { key: 'po_box', label: 'PO BOX' },
  { key: 'locality', label: 'LOCALITY' },
  { key: 'region', label: 'REGION' },
  { key: 'postal_code', label: 'POSTAL CODE' },
  { key: 'country', label: 'COUNTRY' },
];

/** An all-empty form. Used only when the node reports no profile at all. */
export function emptyProfileForm() {
  const form = { address: {}, alternate_names: '', photo_data_url: '' };
  for (const f of PROFILE_FIELDS) form[f.key] = '';
  for (const f of ADDRESS_FIELDS) form.address[f.key] = '';
  return form;
}

/**
 * Build the form state from the node's CURRENT profile JSON
 * (GET /api/node/epm/json — lowercase snake_case, same keys as the PUT
 * body, plus read-only extras this function ignores).
 *
 * Absent key ⇒ empty string. Nothing else is consulted.
 */
export function profileFormFromJson(json) {
  const src = json && typeof json === 'object' ? json : {};
  const form = emptyProfileForm();
  for (const f of PROFILE_FIELDS) {
    if (typeof src[f.key] === 'string') form[f.key] = src[f.key];
  }
  const addr = src.address && typeof src.address === 'object' ? src.address : {};
  for (const f of ADDRESS_FIELDS) {
    if (typeof addr[f.key] === 'string') form.address[f.key] = addr[f.key];
  }
  if (Array.isArray(src.alternate_names)) {
    form.alternate_names = src.alternate_names.filter((v) => typeof v === 'string').join('\n');
  }
  if (typeof src.photo_data_url === 'string') form.photo_data_url = src.photo_data_url;
  return form;
}

/** One alternate name per line; blank lines are not names. */
export function parseAlternateNames(text) {
  return String(text ?? '')
    .split(/\r?\n/)
    .map((v) => v.trim())
    .filter((v) => v.length > 0);
}

/**
 * Serialize the form to the PUT /api/node/epm body.
 *
 * EVERY editable key is present — including empty strings — because the
 * node replaces the profile wholesale: an omitted key would be
 * indistinguishable from "clear this field" only by luck, and a present
 * empty string says exactly what the operator did. Values are trimmed
 * (whitespace normalization, not invention).
 */
export function profileFormToBody(form) {
  const src = form && typeof form === 'object' ? form : {};
  const body = {};
  for (const f of PROFILE_FIELDS) body[f.key] = String(src[f.key] ?? '').trim();
  const address = {};
  const addr = src.address && typeof src.address === 'object' ? src.address : {};
  for (const f of ADDRESS_FIELDS) address[f.key] = String(addr[f.key] ?? '').trim();
  body.address = address;
  body.alternate_names = parseAlternateNames(src.alternate_names);
  // Not edited here, but carried so that saving a name never deletes a photo.
  body.photo_data_url = String(src.photo_data_url ?? '');
  return body;
}

/** True when the form differs from the profile it was loaded from. */
export function profileFormDirty(form, baseline) {
  return JSON.stringify(profileFormToBody(form)) !== JSON.stringify(profileFormToBody(baseline));
}
