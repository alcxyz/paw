#!/usr/bin/env python3
"""Create deterministic PAW closure and container-image measurements."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import tarfile
from typing import Any


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--archive", required=True, type=pathlib.Path)
    parser.add_argument("--oci", required=True, type=pathlib.Path)
    parser.add_argument("--structured-attrs", required=True, type=pathlib.Path)
    parser.add_argument("--name", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    return parser.parse_args()


def read_json(path: pathlib.Path) -> Any:
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def read_blob(oci: pathlib.Path, digest: str) -> Any:
    algorithm, value = digest.split(":", maxsplit=1)
    return read_json(oci / "blobs" / algorithm / value)


def archive_layers(path: pathlib.Path) -> list[dict[str, Any]]:
    with tarfile.open(path, mode="r:*") as archive:
        return [
            {"path": member.name, "uncompressedBytes": member.size}
            for member in archive.getmembers()
            if member.isfile() and member.name.endswith("/layer.tar")
        ]


def main() -> None:
    args = parse_args()
    index = read_json(args.oci / "index.json")
    if len(index["manifests"]) != 1:
        raise ValueError("expected exactly one OCI manifest")

    manifest_descriptor = index["manifests"][0]
    manifest = read_blob(args.oci, manifest_descriptor["digest"])
    config = read_blob(args.oci, manifest["config"]["digest"])
    layers = archive_layers(args.archive)
    largest_layers = sorted(
        layers,
        key=lambda layer: layer["uncompressedBytes"],
        reverse=True,
    )[:20]

    structured_attrs = read_json(args.structured_attrs)
    closure = sorted(
        structured_attrs["runtimeClosure"],
        key=lambda member: member["narSize"],
        reverse=True,
    )
    compressed_registry_bytes = (
        manifest_descriptor["size"]
        + manifest["config"]["size"]
        + sum(layer["size"] for layer in manifest["layers"])
    )

    report = {
        "schemaVersion": 1,
        "image": {
            "name": args.name,
            "tag": args.tag,
            "architecture": config["architecture"],
            "manifestDigest": manifest_descriptor["digest"],
            "archiveBytes": os.stat(args.archive).st_size,
            "uncompressedLayerBytes": sum(
                layer["uncompressedBytes"] for layer in layers
            ),
            "compressedRegistryBytes": compressed_registry_bytes,
            "layerCount": len(layers),
            "largestLayers": largest_layers,
            "runtime": {
                "user": config["config"]["User"],
                "workingDirectory": config["config"]["WorkingDir"],
                "entrypoint": config["config"]["Entrypoint"],
                "command": config["config"]["Cmd"],
                "labels": config["config"].get("Labels", {}),
            },
        },
        "runtimeClosure": {
            "narBytes": sum(member["narSize"] for member in closure),
            "pathCount": len(closure),
            "largestMembers": [
                {"path": member["path"], "narBytes": member["narSize"]}
                for member in closure[:20]
            ],
        },
    }

    args.output.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


if __name__ == "__main__":
    main()
