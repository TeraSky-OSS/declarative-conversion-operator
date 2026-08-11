# Scalar ⇄ Fields

Two mirrored strategies: **`scalarToFields`** (one hub scalar decomposes into several spoke fields) and **`fieldsToScalar`** (several hub fields join into one spoke scalar).

## What it does

Decomposes a single scalar string into several fields using a regular expression with named capture groups (`pattern`), and reassembles it for the reverse direction using a Go [`text/template`](https://pkg.go.dev/text/template) (`joinTemplate`) referencing those same capture-group names.

## When to use it

A single field packs multiple pieces of information into one string on one version (`"50Gi"`, `"Jane Doe <jane@example.com>"`), and a later (or earlier) version splits that same information into separate, properly-typed fields.

!!! lossy "Always lossy unless `losslessOverride: true`"
    The engine can't statically verify that `pattern` and `joinTemplate` are true inverses of each other — it doesn't parse or execute either at compile time — so this strategy is always treated as lossy unless you explicitly set `losslessOverride: true`. Real protection comes from running representative samples through [`convctl test`](../cli.md), not from compile-time analysis: if your pattern and template genuinely round-trip cleanly, `convctl test` will show a clean pass for every sample it's exercised against, which is what backs up the override in practice.

## Example: `scalarToFields`

The hub's `diskSize` (e.g. `"50Gi"`) decomposes into the v2 spoke's separate `diskSizeValue`/`diskSizeUnit`:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        diskSize:
          type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        diskSizeValue:
          type: integer
        diskSizeUnit:
          type: string
    ```

### Rule

```yaml
- strategy: ScalarToFields
  scalarToFields:
    hubPath: spec.diskSize
    pattern: '^(?P<value>\d+)(?P<unit>[A-Za-z]+)$'
    spokeFields:
      value: spec.diskSizeValue
      unit: spec.diskSizeUnit
    joinTemplate: "{{.value}}{{.unit}}"
    losslessOverride: true
```

Each named capture group in `pattern` (`value`, `unit`) maps to a spoke path in `spokeFields`, and is coerced to that field's declared schema type (`diskSizeValue` is captured as a string by the regex but stored as the declared `integer`). `joinTemplate` references the same group names for the reverse direction.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      diskSize: "50Gi"
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      diskSizeValue: 50
      diskSizeUnit: "Gi"
    ```

## Example: `fieldsToScalar`

The structural mirror — the hub's separate `contactName`/`contactEmail` fields join into the v1 spoke's single `contact` string:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        contactName:
          type: string
        contactEmail:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        contact:
          type: string
    ```

### Rule

```yaml
- strategy: FieldsToScalar
  fieldsToScalar:
    hubFields:
      name: spec.contactName
      email: spec.contactEmail
    pattern: '^(?P<name>[^<]+) <(?P<email>[^>]+)>$'
    spokePath: spec.contact
    joinTemplate: "{{.name}} <{{.email}}>"
    losslessOverride: true
```

Here `hubFields` maps each named group to the hub path it reads from for the **join** direction (hub → spoke); `pattern` decomposes the spoke scalar back into those same group names for the **split** direction (spoke → hub).

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      contactName: "Jane Doe"
      contactEmail: "jane@example.com"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      contact: "Jane Doe <jane@example.com>"
    ```
