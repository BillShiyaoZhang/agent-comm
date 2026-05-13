#!/usr/bin/env python3
"""Revoke the current pending one-time token (e.g., suspect abuse or changed mind)."""

import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import one_time_token as token_lib


if __name__ == "__main__":
    if token_lib.revoke_token():
        print("Token revoked.")
    else:
        print("No pending token to revoke.")