# Implementation Notes

Claude Designer output should be translated back into the Svelte app under
`sdn-js/ui/src`.

Recommended implementation order after design approval:

1. Extract reusable shell/navigation changes into `components/AppShell.svelte`,
   `components/SideNav.svelte`, and `components/TopStatusBar.svelte`.
2. Update CSS tokens in `styles/tokens.css`.
3. Update shared layout/component styles in `styles/app.css`.
4. Implement screen-level changes one route at a time.
5. Preserve backend calls and data contracts unless the approved design lists a
   backend implication.
6. Rebuild the SDN UI with `npm --prefix sdn-js run build:sdn-ui`.
7. Run focused SDN UI tests and Desktop route tests before packaging Desktop.

The prototype uses fixture data and does not define production API contracts.
