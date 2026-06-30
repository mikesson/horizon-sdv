# Spanner schema: applied declaratively at deploy

## What ABFS actually does (and doesn't)

The ABFS server does **not** create or migrate the Spanner schema at runtime. The
binary (verified against image `abfs-alpha:0.1.14`) links no Spanner DDL-admin API
and embeds no schema. Pointed at an empty database, every operation fails with
`Table not found`. ABFS is a **schema consumer, not a schema owner**: the tables,
sequences, indexes, and foreign keys must already exist before the server starts.

> Earlier revisions of this document claimed ABFS applied DDL at runtime and that
> `spec.ddl` should be left empty. That was incorrect and has been corrected here.

So the deploy applies the schema, exactly as the sibling Terraform module does.

## How the Terraform module does it (the model we mirror)

The upstream `terraform-google-abfs` module ships the schema as a file
(`modules/server/files/schemas/0.0.31-schema.sql` — 7 tables: `Objects`, `Links`, `Chunks`,
`ChunkTouches`, `Maps`, `Projects`, `Refs`, plus bit-reversed sequences, indexes,
and foreign keys). `spanner.tf` reads that file, splits it into statements, and
feeds them into `google_spanner_database.ddl`:

- gated by the variable `abfs_spanner_database_create_tables` (default `false`):
  when `false`, the `ddl` list is empty;
- with `lifecycle { ignore_changes = [ddl] }` so Terraform applies the schema once
  at create time and then never touches it again.

## How the KCC port mirrors it

The same schema is bundled at
[`infra/schemas/0.0.31-schema.sql`](../infra/schemas/0.0.31-schema.sql) and
mirrored into [`infra/20-spanner.yaml`](../infra/20-spanner.yaml) as
`SpannerDatabase.spec.ddl` — **one `CREATE` statement per list item** (KCC's
`SpannerDatabase` takes `ddl` as a list of statements, no trailing semicolons).

```yaml
# infra/20-spanner.yaml (excerpt)
apiVersion: spanner.cnrm.cloud.google.com/v1beta1
kind: SpannerDatabase
metadata:
  name: abfs
  annotations:
    cnrm.cloud.google.com/deletion-policy: "abandon"   # protect the database from deletion
spec:
  instanceRef:
    name: abfs
  # ABFS schema (mirrors infra/schemas/0.0.31-schema.sql). render.sh replaces this
  # whole block with `ddl: []` when CREATE_TABLES != true.
  ddl:                                                  # ABFS-DDL-BEGIN
    - 'CREATE TABLE Objects (ObjectId STRING(128), ... ) PRIMARY KEY (ObjectId)'
    - 'CREATE SEQUENCE LinkIdSequence OPTIONS (sequence_kind="bit_reversed_positive")'
    # ... remaining tables, sequences, indexes, foreign keys ...
  # ABFS-DDL-END
```

### The `CREATE_TABLES` toggle

The Terraform `abfs_spanner_database_create_tables` flag maps to the instance-env
toggle **`CREATE_TABLES`** (read by [`scripts/render.sh`](../scripts/render.sh),
not a `REPLACE_*` token; default `false`). The worked example
[`instances/example.env`](../instances/example.env) sets it `true`:

- **`CREATE_TABLES=true`** — `render.sh` keeps the `spec.ddl` block as-is, so KCC
  creates the database **and** applies the bundled schema in one apply.
- **`CREATE_TABLES != true`** — `render.sh` replaces the block between the
  `# ABFS-DDL-BEGIN` / `# ABFS-DDL-END` markers with `ddl: []`, so KCC creates an
  empty database shell and you apply the schema another way (see below). This is
  the KCC equivalent of `abfs_spanner_database_create_tables = false`.

## Do not edit `spec.ddl` after first apply

Once the database exists, **treat `spec.ddl` as immutable**. Editing or reordering
it can drop tables and destroy data. This is the KCC equivalent of Terraform's
`ignore_changes = [ddl]` — there it is enforced by the lifecycle block; here it is
a discipline you hold (KCC has no per-field "ignore drift" for `ddl`). Schema
evolution is handled by ABFS's own online-DDL tooling against the live database,
not by re-applying this manifest. The `deletion-policy: abandon` annotation
protects the database itself from CR deletion.

## Out-of-band schema apply (when `CREATE_TABLES=false`)

If you keep `CREATE_TABLES=false` (e.g. you seed the schema separately, or the
database already exists), apply the bundled DDL directly with `gcloud`:

```bash
gcloud spanner databases ddl update abfs \
  --instance=abfs --project=PROJECT_ID \
  --ddl-file=infra/schemas/0.0.31-schema.sql
```

The `.sql` file is the authoritative, semicolon-delimited form; the `spec.ddl`
list in `20-spanner.yaml` mirrors it. Keep the two in sync if you edit either.

## Schema versioning

The `0.0.31` in the filename tracks the **ABFS server version line** the schema
matches (the same version the Terraform module ships). If you run a newer ABFS
server whose schema differs, bump the bundled schema: add the new
`infra/schemas/<ver>-schema.sql`, regenerate the `spec.ddl` block in
`20-spanner.yaml` from it, and apply the delta to existing databases out-of-band
(ABFS does not migrate them for you).

## Net

- `SpannerInstance` → KCC-owned (config, autoscaling, edition, backups).
- `SpannerDatabase` → KCC-owned, **including the schema** in `spec.ddl`, gated by
  `CREATE_TABLES` (mirrors the Terraform module). Empty shell only when
  `CREATE_TABLES != true`.
- **The ABFS server does not self-migrate** — it consumes a schema the deploy
  applied. Don't edit `spec.ddl` after creation.
