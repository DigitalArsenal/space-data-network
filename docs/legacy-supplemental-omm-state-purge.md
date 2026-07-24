# Legacy Supplemental OMM state purge

The source purge does not edit a node's existing repository. A node that was
previously configured with the Supplemental OMM or CelesTrak reference flows
can otherwise restart their persisted timers even after the installers are
removed from source.

Use `scripts/purge-legacy-supplemental-omm-state.mjs` during the deployment of
the zero-app-specific-Go build. The migration is dry-run by default. It removes
only the exact legacy module/flow identities and the dedicated
`role:omm`/`role:celestrak` provenance, captures portable artifact hashes from
the registries and referenced runtimes, revokes matching capability approvals,
and quarantines matching timer configs, in-repository flow bundles, module
drop-ins, and the CelesTrak fetch ledger.

## Required sequence

1. Install a build whose production HTTP capability denies CelesTrak before
   DNS or transport, but do not start it yet.
2. Stop the node and confirm it is no longer writing its repository.
3. Run the read-only inventory:

   ```sh
   node scripts/purge-legacy-supplemental-omm-state.mjs \
     --repo /absolute/path/to/node-repo \
     --json
   ```

4. Review `legacyModules`, `legacyFlows`, `legacyHashes`,
   `approvalsToRevoke`, `filesToQuarantine`, and `externalRefs` in the report.
   External bundle references are disabled by registry removal but are not
   deleted outside the node repository.
5. Apply with a new backup path outside the node repository:

   ```sh
   node scripts/purge-legacy-supplemental-omm-state.mjs \
     --repo /absolute/path/to/node-repo \
     --apply \
     --backup-dir /absolute/path/to/new-backup-directory \
     --json
   ```

   The command refuses an existing backup directory and completes the recovery
   copy before changing live state.
6. Run the dry-run command again. It must report `"clean": true` with no
   legacy entries, approvals, or files.
7. Start the node. Confirm its generic module/flow scheduler inventories do not
   contain any of these IDs:

   - `org.sdn.flows.od-supplemental-omm`
   - `com.digitalarsenal.flows.celestrak-gp-ingest`
   - `com.digitalarsenal.flows.celestrak-satcat-ingest`
   - `com.digitalarsenal.flows.celestrak-spw-ingest`
   - `com.orbpro.celestrak-supgp`
   - `com.orbpro.gps-source`

8. Verify outbound monitoring records no DNS or HTTP attempt to a CelesTrak
   domain.

Do not run this migration against a live node. No production repository is
modified by the source build or its tests.
