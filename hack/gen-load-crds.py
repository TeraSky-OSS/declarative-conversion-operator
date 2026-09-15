#!/usr/bin/env python3
"""Emit N CustomResourceDefinitions with large OpenAPI schemas.

Used by hack/measure-cache-memory.sh to make a cluster look like a mature
Crossplane installation, where the CRD informer's cost is dominated by
schemas rather than by object headers. Each CRD here carries ~200 leaf
properties across two versions, which is the right order of magnitude for a
composite resource generated from a real XRD.
"""
import sys

def schema(leaves):
    props = {}
    for i in range(leaves):
        props[f"field{i}"] = {
            "type": "string",
            "description": "A representative property description, long "
                           "enough that descriptions are a real share of "
                           "the object's bytes, which on generated CRDs "
                           "they are.",
        }
    return props

def crd(n, leaves=200):
    props = schema(leaves)
    versions = []
    for v in ("v1alpha1", "v1beta1"):
        versions.append({
            "name": v,
            "served": True,
            "storage": v == "v1alpha1",
            "schema": {"openAPIV3Schema": {
                "type": "object",
                "properties": {"spec": {"type": "object", "properties": props}},
            }},
        })
    return {
        "apiVersion": "apiextensions.k8s.io/v1",
        "kind": "CustomResourceDefinition",
        "metadata": {
            "name": f"memloads{n}.memload.example.org",
            "labels": {"dco.terasky.com/memload": "true"},
        },
        "spec": {
            "group": "memload.example.org",
            "scope": "Namespaced",
            "names": {
                "plural": f"memloads{n}",
                "singular": f"memload{n}",
                "kind": f"MemLoad{n}",
            },
            "versions": versions,
        },
    }

def main():
    import json
    count = int(sys.argv[1]) if len(sys.argv) > 1 else 300
    docs = [crd(i) for i in range(count)]
    print(json.dumps({"apiVersion": "v1", "kind": "List", "items": docs}))

if __name__ == "__main__":
    main()
