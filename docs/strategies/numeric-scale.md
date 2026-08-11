# Numeric Scale

## What it does

Rescales a numeric field by a fixed factor between hub and spoke: `hubValue == spokeValue * factor`.

## When to use it

The same quantity is stored at different units on different versions — megabytes stored on the hub, displayed as gigabytes on a spoke, for instance.

!!! conditional-lossy "Lossy on whichever side is integer-typed"
    Dividing or multiplying by `factor` doesn't necessarily land on a whole number for every possible input. Whichever side is declared as an integer type in its schema is the one that can lose precision (a value that isn't an exact multiple of `factor` gets rounded when displayed on that side) — that direction requires `acknowledgeLossy: true`. If both sides are non-integer (`number`) types, the conversion is exact in both directions.

## Example

The hub's `memoryMB` (integer) is displayed as `memoryGB` (also integer) on the v2 spoke, with `factor: 1024`:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        memoryMB:
          type: integer
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        memoryGB:
          type: integer
    ```

### Rule

```yaml
- strategy: NumericScale
  numericScale:
    hubPath: spec.memoryMB
    spokePath: spec.memoryGB
    factor: 1024
  acknowledgeLossy: true
  reason: >-
    both memoryMB and memoryGB are integers, so a memoryMB value that
    isn't an exact multiple of 1024 rounds when displayed as GB.
```

### Objects

An exact multiple of 1024 round-trips perfectly:

=== "Hub (v3)"

    ```yaml
    spec:
      memoryMB: 4096
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      memoryGB: 4
    ```

A value that isn't an exact multiple (e.g. `memoryMB: 3000`) still converts (`spec.memoryGB` comes out rounded), but converting that rounded value back up to `memoryMB` won't reproduce the original `3000` exactly — this is precisely the acknowledged-lossy direction, and running representative samples through [`convctl test`](../cli.md) is how you'd catch it if your real data isn't always a clean multiple of `factor`.
