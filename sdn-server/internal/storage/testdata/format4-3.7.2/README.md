# Real format-4 migration fixture

`store/fsql4` was created, activated, closed, reopened, and queried by FlatSQL 3.7.2, commit `1b3ae18c00bb7a8917acc3db32b903d6ae8affea`. No marker bytes were relabeled. The store contains only synthetic fixture records.

`fixture-gen.cpp` links against that commit's native `flatsql_p4_core`, `sqlite3_p4`, `cpp/tests/p4/p4_test_main.cpp`, native host I/O, and the pinned FlatBuffers compiler support. Run it with `--test=generate_format4_fixture --store=<new-directory> --bfbs=<sdn-server/internal/sds/search-schemas>`. It refuses an existing output and checks every PUT result for rejection. Its generated BFBS specifications use the node's canonical schemas.

The old engine captures hydrated GET with every copy, TAGS, HEAD, type counters, producer counters, and instance counters into `expected-raw.json`. `expected.json` maps those typed cells into the Go API structs: underscores in column names are removed, blob bytes become JSON base64, record provenance is nested under Tag, and nullable epochs also set HasEpoch. Row order is ignored in the test; every exposed field is compared.

The fixture includes three distinct OMM records, a second producer's copy, two tag instances per record, a synthetic sealed SDF1 payload, signature bytes, and an IQC ingest identity. The signature and envelope bytes are synthetic input; no signing or encryption operation ran. The test also creates a valid auxiliary publication journal before first boot and checks publication fields, feed sequence/head, source-file immutability, rollback metadata, corrupt-source refusal, and idempotence.
