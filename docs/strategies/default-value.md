# Default Value

## What it does

Injects a default value for a field that exists on only one side (typically a field that's new in a later version) when converting *into* the side that lacks it. The direction converting *away from* the side that has it drops the real value — there's nowhere on the other side to put it.

## When to use it

A field is genuinely new (or genuinely retired) between versions, has no equivalent on the other side, but you want objects converted into the side missing it to get a sane, working default rather than the field simply being absent — for instance, filling a new required field so converted objects still validate.

!!! conditional-lossy "One direction is always lossy"
    If `existsOn: Spoke` (the field is new on the spoke, absent on the hub): converting **hub → spoke** injects the default (lossless — the hub never had a real value to preserve) and converting **spoke → hub** drops the real spoke value (lossy — the hub has nowhere to put it). This is inherent to the strategy, not a configuration mistake — `acknowledgeLossy: true` is always required.

## Example

`computeUnits` exists only on the v1 spoke — the hub schema has no such field at all:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        # no computeUnits field here
        storageGB:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        computeUnits:
          type: integer
    ```

### Rule

```yaml
- strategy: DefaultValue
  defaultValue:
    path: spec.computeUnits
    existsOn: Spoke
    default: 1
  acknowledgeLossy: true
  reason: >-
    computeUnits only exists on v1; converting a v1 object up to the hub
    and back down is fine, but hub-native objects converted down to v1
    get a sane default and lose nothing on the way back since the hub
    never had a real value to begin with.
```

### Objects

A hub-native object, converted down to v1, gets the default injected:

=== "Hub (v3)"

    ```yaml
    spec:
      storageGB: "500"
      # (no computeUnits — the hub schema doesn't have this field)
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      storageGB: "500"
      computeUnits: 1   # <- injected default
    ```

A v1-native object with a real `computeUnits` value, converted up to the hub, simply loses it (there's no hub field to put it in) — this is the acknowledged-lossy direction.

`existsOn: Hub` is the mirror case: the field exists only on the hub, and converting spoke → hub injects the default while hub → spoke drops the real value.
