# Fields ⇄ Map

Two mirrored strategies: **`fieldsToMap`** (several hub sibling fields aggregate into one spoke map) and **`mapToFields`** (a hub map splits into several spoke sibling fields).

## What it does

Consolidates a fixed, known set of sibling fields into a single free-form map (or the reverse) — the classic "these three separate fields should really just be one config map" API-evolution pattern.

## When to use it

You're aggregating a small, fixed number of named fields into a map on one side, or splitting a map's known keys back out into individual fields on the other.

!!! conditional-lossy "Depends on `onUnknownSpokeKey`/`onUnknownHubKey`"
    The direction that **splits the map into fields** is lossless only if every key actually present in the map is one you've declared — an unknown key with `on...Key: Error` (the default) makes that direction fail closed at validation time (a genuine schema mismatch, not silently dropped); `on...Key: Drop` makes it lossy instead (any config using it needs `acknowledgeLossy: true`). The direction that **aggregates fields into the map** is always lossless — nothing is dropped, since every source field is explicitly named in `hubPaths`/`spokePaths`.

## Example: `fieldsToMap`

The hub's separate `cpuLimit`/`memoryLimit` fields aggregate into a single `limits` map on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        cpuLimit:
          type: string
        memoryLimit:
          type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        limits:
          type: object
          additionalProperties:
            type: string
    ```

### Rule

```yaml
- strategy: FieldsToMap
  fieldsToMap:
    hubPaths:
      - spec.cpuLimit
      - spec.memoryLimit
    spokeMapPath: spec.limits
    keyNames:
      spec.cpuLimit: cpu
      spec.memoryLimit: memory
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      cpuLimit: "4"
      memoryLimit: "16Gi"
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      limits:
        cpu: "4"
        memory: "16Gi"
    ```

## Example: `mapToFields`

The structural mirror — the hub's `tags` map splits into separate `envTag`/`teamTag` fields on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        tags:
          type: object
          additionalProperties:
            type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        envTag:
          type: string
        teamTag:
          type: string
    ```

### Rule

```yaml
- strategy: MapToFields
  mapToFields:
    hubMapPath: spec.tags
    spokePaths:
      - spec.envTag
      - spec.teamTag
    keyNames:
      spec.envTag: env
      spec.teamTag: team
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      tags:
        env: "prod"
        team: "platform"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      envTag: "prod"
      teamTag: "platform"
    ```

Here, converting **hub → spoke** splits the map (lossless only if `tags` never contains a key besides `env`/`team`); converting **spoke → hub** aggregates the two fields back into the map (always lossless).
