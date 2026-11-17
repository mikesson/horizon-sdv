# API Documentation

The native API lives at **`/v2`**. Every JSON endpoint is a
[Huma](https://huma.rocks) operation, so the OpenAPI specification and the
interactive reference are generated from the code and can never drift from
it. (The legacy `/v1` compatibility surface is documented separately; see
[Legacy API](legacy-api.md).)

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `/docs` | Interactive API reference ([Scalar](https://github.com/scalar/scalar), served by a custom route in `internal/router/docs.go`) |
| `/openapi.json` / `/openapi.yaml` | OpenAPI 3.1 specification |
| `/openapi-3.0.json` / `/openapi-3.0.yaml` | OpenAPI 3.0.3 specification |
| `/schemas/{name}` | Individual JSON schemas |

**Authentication is required for all of these.** The only public paths on
the service are the Cloud Run health probes `/health` and `/ready`; the
docs, specs, and schemas sit behind the same IAP gate as the API itself.
Open `/docs` in a signed-in browser, or fetch `/openapi.json` with a
bearer token.

The console links to `/docs` from the sidenav's Developer section.

## Conventions

- Every operation carries an `OperationID`, tags, and an `Errors` list of
  the business error codes it can actually return; errors are
  [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem details.
- Requests are validated against the JSON Schema generated from the input
  structs; unknown body fields are rejected.
- Mutating creates accept an `Idempotency-Key` header (hosts, devices,
  batches) making retries safe.
- `GET /v2/devices` and `GET /v2/builds` return `next_page_token`, empty on
  the last page. Pass it back as `page_token` to read the next page. A token
  walk does not repeat or skip rows when rows are created or deleted between
  requests, which `offset` paging can. A request with both `page_token` and
  a non-zero `offset` is a 400. `GET /v2/hosts` pages by `offset` only.
- `GET /v2/storage/buckets/{bucket}/objects` returns the whole folder level
  by default. With `page_size` set it returns one page and a
  `next_page_token` for the next `page_token`.
- Every `/v2/admin/system-config/` GET returns an `ETag`. A PUT or DELETE
  that sends it back as `If-Match` fails with 412 and writes nothing when the
  setting changed since that read. Without `If-Match` the write is
  unconditional. All fleet writes share one tag, and each write returns the
  new one.
- The only non-Huma routes under `/v2` are the ones OpenAPI cannot express:
  the WebRTC signaling long-poll + WebSocket, the ADB byte stream
  (`GET /v2/devices/{id}/adb`, a WebSocket carrying the ADB wire protocol),
  and storage object byte streams.

## Schema drift gates

Both clients are held to the spec:

- The console's Zod schemas are checked against a freshly generated spec
  (`npm run check:schema-drift`, backed by `tests/specgen`), and the browser
  suite's mocked responses are parsed through those same schemas
  (`npm run check:fixture-shapes`, a `npm run lint` leg), so spec, schema and
  fixture cannot drift apart without a gate going red.
- `cfctl`'s response types are generated from the spec
  (`scripts/generate-ccogen.sh`, committed at
  `cmd/cfctl/internal/cco/ccogen/`); the `gen` job in `scripts/ci.sh`
  regenerates and diffs, so a schema change without a regenerate fails the
  build. The few hand-written remainders, the operation handle and the
  scalar system-config values, are still checked reflectively against the
  server's structs (`cmd/cfctl/internal/cco/drift_test.go`).

## References

- [Huma OpenAPI Generation](https://huma.rocks/features/openapi-generation/)
- [Legacy API compatibility](legacy-api.md)
