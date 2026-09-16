# Branch Map

## What it does

Maps the branches of a **union-typed field** — the "one of `s3`, `gcs` or
`azure`" shape mature platform APIs express with `oneOf` — between the hub
and a spoke. It writes the branch that corresponds to whichever one the
input has set, remaps an optional discriminator alongside it, and can run
nested rules scoped to that branch's own subtree.

## When to use it

A union-typed field whose **branch names or branch shapes differ between
versions**.

You do not need it for a union whose branches happen to keep the same names
and shapes — those are ordinary declared fields and ordinary rules reach
them. What `branchMap` adds is the *correspondence*: a declaration of which
hub branch becomes which spoke branch, checked at compile time and enforced
at runtime.

!!! conditional-lossy "Lossless unless branches collapse"
    Mapping each hub branch to a distinct spoke branch is lossless in both
    directions, plus whatever the nested rules contribute. Mapping **two
    hub branches onto one spoke branch** is lossy coming back — the engine
    cannot tell which one it started from — and needs
    `acknowledgeLossy: true`, the same verdict `enumRemap` gives a
    non-injective mapping.

    A hub branch the rule does not name at all is not a lossiness question:
    its fields are left uncovered, which is an ordinary validation error.
    Map it, or `delete` it deliberately.

## What a union looks like in a CRD

This matters, because it is narrower than general JSON Schema and it is
what the strategy is built against.

The apiserver requires **every property named inside a `oneOf` to also be
declared in the parent's own `properties`**. So a union is a set of
declared, optional, mutually-exclusive fields, and the `oneOf` only says
which of them may be set. A branch is not a hidden shape — it is a
property, with a path, that ordinary rules could already address. This is
pinned against the apiserver's own validator in
[`pkg/engine/structural_facts_test.go`](https://github.com/TeraSky-OSS/declarative-conversion-operator/blob/main/pkg/engine/structural_facts_test.go).

That is why the active branch is identified by **which branch property is
present** rather than by validating the object against each branch schema.
Structural matching would mean running a full JSON Schema validator on the
apiserver's admission path to learn something a map lookup already knows.

## Example

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        store:
          type: object
          properties:
            backend:
              type: string
            s3:
              type: object
              properties:
                bucket:
                  type: string
            gcs:
              type: object
              properties:
                bucket:
                  type: string
          oneOf:
            - required: ["s3"]
            - required: ["gcs"]
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        store:
          type: object
          properties:
            backend:
              type: string
            objectStore:
              type: object
              properties:
                name:
                  type: string
            googleStore:
              type: object
              properties:
                name:
                  type: string
          oneOf:
            - required: ["objectStore"]
            - required: ["googleStore"]
    ```

### Rule

```yaml
- strategy: BranchMap
  branchMap:
    hubPath: spec.store        # the union OBJECT, not a branch within it
    spokePath: spec.store
    discriminator: backend     # optional
    branches:
      - hubBranch: s3
        spokeBranch: objectStore
        rules:                 # paths are relative to the branch
          - strategy: FieldRename
            fieldRename:
              hubPath: bucket
              spokePath: name
      - hubBranch: gcs
        spokeBranch: googleStore
        rules:
          - strategy: FieldRename
            fieldRename:
              hubPath: bucket
              spokePath: name
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      store:
        backend: s3
        s3:
          bucket: logs
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      store:
        backend: objectStore
        objectStore:
          name: logs
    ```

## No branch, or several, is a hard error

Converting an object with **no** branch set, or with **more than one**,
fails the conversion with a message naming the paths — in both directions.

That is the fail-closed posture the rest of the engine uses, and here it
buys something specific. Without it, an object with no branch set would
convert to an object with no branch set, and the *destination's* own
`oneOf` would reject it at admission — with a message about the schema,
arriving after the conversion that produced it, pointing at the wrong
thing. An object with two branches set is the shape a `oneOf` added after
the fact leaves behind in already-stored objects.

## The discriminator

`discriminator` names a sibling property whose value also identifies the
branch. It is remapped alongside, so hub and spoke may spell their branch
names differently:

```yaml
    discriminator: backend
    branches:
      - hubBranch: s3
        spokeBranch: objectStore
        # Defaults to the branch names themselves, which is usually right.
        hubDiscriminatorValue: s3
        spokeDiscriminatorValue: objectStore
```

It must be a declared property of both unions. Leave it out when the union
has no such field, or when you would rather map it yourself with
[`enumRemap`](enum-remap.md) — `branchMap` claims the discriminator only
when it is configured to manage it.

## Coverage and ordering

Each mapped branch is claimed as a subtree on both sides, and each branch
pair's nested rules are resolved against the two branch schemas — the same
scoping [`forEach`](for-each.md) gives an array element. A leaf inside a
branch that no nested rule covers is reported like any other uncovered
field, qualified with the branch path.

`branchMap` writes each branch at its own path rather than replacing the
union object. A union object may carry properties that are not branches at
all — a retention period alongside `s3` and `gcs` — and those are covered
by ordinary rules. Writing the object wholesale would make the result
depend on the order the two rules were declared in, which nothing else in
this engine does.

## Field reference

| Field | Meaning |
|---|---|
| `hubPath` / `spokePath` | The union-typed **object** on each side, not a branch within it. |
| `discriminator` | Optional sibling property naming the active branch. Must be declared on both sides. |
| `branches[].hubBranch` / `.spokeBranch` | Property names inside the two unions. Both must be declared properties. |
| `branches[].hubDiscriminatorValue` / `.spokeDiscriminatorValue` | Values written to the discriminator. Default to the branch names. |
| `branches[].rules` | Rules for this branch pair, with paths relative to the branch. |
