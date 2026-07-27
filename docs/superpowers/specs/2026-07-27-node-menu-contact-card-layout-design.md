# Node Menu Contact Card Layout Design

Date: 2026-07-27
Status: approved for implementation

## Goal

Simplify the `THIS NODE` widget page around one predictable contact summary.
The first widget is a Contact Card that always shows the same six fields, and
every remaining widget participates in one responsive sizing and spacing model
without allowing long identifiers or addresses to overflow its card.

## Current Surface

`sdn-js/dashboard/src/App.svelte` renders `NodeWidgets.svelte` on the
`THIS NODE` route. `NodeWidgets.svelte` currently owns:

- Status & Identity;
- Verification Keys;
- EPM Provenance;
- Chain Addresses;
- a wide, mode-switching Contact Card;
- wide Network Addresses.

Contact and identity values are derived from the node status view model and the
node's published vCard through `sdn-js/dashboard/src/vcard.js`.

## Design

### Contact Card

Replace the first Status & Identity widget and the existing wide Contact Card
with one Contact Card at the start of the widget grid. Do not render a duplicate
Contact Card elsewhere on the page.

The card always renders these rows in this order:

1. First name
2. Last name
3. Organization
4. Phone number
5. Address
6. Email address

Use only human contact values from the parsed vCard:

| Row | vCard source |
| --- | --- |
| First name | First non-empty given-name component (index 1) found while scanning `N` properties in order |
| Last name | First non-empty family-name component (index 0) found while scanning `N` properties in order |
| Organization | First non-empty `ORG` property |
| Phone number | First non-empty `TEL` property |
| Address | First non-empty `ADR` property, with empty components removed and remaining components joined by `, ` |
| Email address | First non-empty `EMAIL` property that is not an SDN machine alias |

Trim values before deciding whether they are present. If the selected source is
missing or trims to an empty string, render the exact fallback text `Unknown`.
Do not infer names from `FN` or `node.dn`, do not infer organization from
`node.org`, and do not expose SDN identity-alias email addresses as contact
email. First and last names scan their structured-name components
independently. For every other repeated property, the first non-empty eligible
value wins so each required row remains singular and stable.

The Contact Card is a static summary. Remove the current parsed/raw/QR view
switcher and its inline QR state from this page. Keep the existing VCF download
and conditional EPM download actions in the Contact Card header; neither action
may replace or hide the six rows. The compact `NodeDetail` dialog used from the
account/node modal is outside this change and keeps its existing vCard and
export behavior.

### Widget Set

After the consolidation, `NodeWidgets.svelte` contains these widgets in DOM and
visual flow order:

1. Contact Card, always rendered;
2. Verification Keys, rendered when it has rows;
3. Chain Addresses, rendered when it has rows;
4. Network Addresses, rendered when the node has addresses.

Remove the EPM Provenance widget, its derived row model, and code used only by
that widget. Do not rename or relocate EPM data in another widget.

The `THIS NODE` page header in `App.svelte`, including display name,
organization, SELF/online state, and Edit action, remains unchanged. Stored
wallets below `NodeWidgets` also remain unchanged.

### Responsive Layout And Containment

All four widgets use the same grid item wrapper and the same header, body, row,
label, value, padding, and gap rules. Remove the special full-row `wide` layout
for Contact Card and Network Addresses.

The grid uses row-major CSS Grid flow with repeatable columns whose minimum is
capped by the available width, so it can collapse to one column without a
widget forcing horizontal page overflow. Cards may have different content
heights; they must share width and spacing behavior rather than forced equal
heights.

Containment is required at every nested boundary:

- the grid, grid item, panel content, header, body, row, and value containers
  use `min-width: 0` where flex/grid sizing could otherwise preserve intrinsic
  width;
- arbitrary identifiers, public keys, chain addresses, emails, and libp2p
  multiaddrs wrap with `overflow-wrap: anywhere`;
- preformatted multi-line values preserve intended line breaks but still wrap;
- labels have a shared desktop width and stack above values at the narrow-card
  breakpoint;
- header actions or chips that remain in other widgets wrap inside the header;
- no widget introduces its own horizontal scrollbar.

Define the repeated minimum card width, grid gap, card padding, row gap, and
label width once in the component style block, preferably as component-scoped
custom properties, and consume those values across every widget.

## Implementation Boundaries

Expected implementation locations:

- `sdn-js/dashboard/src/NodeWidgets.svelte`
  - replace the first widget;
  - remove the duplicate Contact Card and EPM Provenance widget;
  - remove obsolete parsed/raw/QR state, QR rendering, and imports while
    retaining the VCF/EPM download path;
  - apply the shared grid and containment styles.
- `sdn-js/dashboard/src/vcard.js`
  - add a small pure helper that produces the six ordered Contact Card rows
    from parsed vCard properties, including `Unknown` fallbacks and machine
    email filtering.
- `sdn-js/dashboard/src/dashboard-logic.test.js`
  - add focused unit coverage for the helper.

No server route, FlatBuffer schema, EPM persistence, node-status model,
authentication, edit form, account modal, Desktop mirror, or design-system
package change is required.

## Test Plan

### Unit

Extend `dashboard-logic.test.js` with:

- a complete vCard proving `N` maps family/given names into Last/First rows,
  address components are formatted, and all six values preserve the required
  order;
- a partial or empty vCard proving all six rows still render and every missing
  value is exactly `Unknown`;
- repeated properties proving the first non-empty eligible value wins;
- a card containing SDN alias emails before a human email proving aliases are
  ignored;
- whitespace-only fields proving they are treated as absent.

Run:

```sh
cd sdn-js
npx vitest run dashboard/src/dashboard-logic.test.js
```

### Build

Build the single-file dashboard to catch Svelte syntax, obsolete imports, and
bundling regressions:

```sh
cd sdn-js
npm run build:dashboard
```

Because that command refreshes the embedded dashboard artifact and CSP, the
implementation diff must include the generated files it intentionally updates
and no unrelated generated changes.

### Browser

Serve the built dashboard with deterministic `THIS NODE` fixture data and
inspect at desktop, two-column transition, and phone widths, including at least
1440 px, 768 px, and 390 px viewports. Verify:

- Contact Card is first and contains exactly the six required rows;
- missing values display `Unknown`;
- EPM Provenance and the duplicate Contact Card are absent;
- optional widgets close their gaps naturally when omitted;
- long xpubs, public keys, chain addresses, email addresses, and multiaddrs stay
  inside their cards;
- no horizontal page or card scrollbar appears;
- the page header, Edit action, compact node modal, and stored-wallet section
  still behave as before.

## Acceptance Criteria

- One Contact Card is the first widget on `THIS NODE`.
- It always renders the six required labels in the specified order.
- Every absent required value displays exactly `Unknown`.
- No SDN machine alias is shown as the contact email.
- VCF and conditional EPM downloads remain available without hiding the
  required Contact Card rows.
- Status & Identity, the old duplicate Contact Card, and EPM Provenance are not
  rendered in `NodeWidgets`.
- Verification Keys, Chain Addresses, and Network Addresses retain their
  existing data and conditional visibility.
- Every remaining widget follows the shared responsive sizing and spacing
  model.
- Long content wraps inside its card at desktop, transition, and phone widths.
- Focused dashboard unit tests and the dashboard build pass.

## Non-Goals

- Changing vCard generation, EPM records, or server APIs.
- Changing the `THIS NODE` page header or identity editor.
- Changing `NodeDetail.svelte` or the compact account/node modal.
- Redesigning shared `Panel`, `Kicker`, or `StatusChip` components.
- Refactoring unrelated dashboard routes or styles.
