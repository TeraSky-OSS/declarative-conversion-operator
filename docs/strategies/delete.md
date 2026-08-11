# Delete

## What it does

Intentionally drops a field that exists on only one side — no injection, no substitute value, just gone on the other side.

## When to use it

A field is a legacy escape hatch, debug flag, or otherwise genuinely has no business existing past a certain version, and you want to explicitly document that it's dropped rather than leaving it as an accidentally-uncovered field (which would fail validation instead).

!!! lossy "Always lossy on the direction the field exists"
    Converting away from the side that has the field always discards real data — `acknowledgeLossy: true` is always required. The opposite direction (converting *into* the side that has the field) is a no-op, since there's nothing to convert *from*.

## Example

`debugMode` is a v1-only legacy field with no hub equivalent at all:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        # no debugMode field
        storageGB:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        debugMode:
          type: boolean
    ```

### Rule

```yaml
- strategy: Delete
  delete:
    path: spec.debugMode
    existsOn: Spoke
  acknowledgeLossy: true
  reason: >-
    debugMode is a v1-only legacy escape hatch with no hub equivalent;
    intentionally dropped when converting up to the hub.
```

### Objects

=== "Spoke (v1)"

    ```yaml
    spec:
      storageGB: "64"
      debugMode: true
    ```

=== "Hub (v3)"

    ```yaml
    spec:
      storageGB: "64"
      # debugMode is gone -- intentionally, not silently
    ```

Converting the resulting hub object back down to v1 produces no `debugMode` at all (the hub → spoke direction is a no-op for this field, since the hub never carries it) — a fresh v1-native object would need to set `debugMode` itself if it wants a non-default value.

!!! warning
    If the field being deleted is listed in that side's schema `required`, the compiler emits a warning: the converted object on that side will fail apiserver validation unless the schema also declares a default for it. `delete` doesn't check this for you — a required field genuinely can't just disappear from a valid object.
