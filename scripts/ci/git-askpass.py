#!/usr/bin/env python3
"""Minimal non-interactive askpass helper for GitHub dependency updates."""

import os
import sys


def main() -> int:
    prompt = sys.argv[1] if len(sys.argv) > 1 else ""
    if "username" in prompt.lower():
        print("x-access-token")
        return 0
    if "password" in prompt.lower():
        token = os.environ.get("GITHUB_TOKEN")
        if not token:
            return 1
        print(token)
        return 0
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
