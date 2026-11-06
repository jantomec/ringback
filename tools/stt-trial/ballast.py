#!/usr/bin/env python3
"""Holds some GPU memory until it's stopped: the room a text-to-speech model would take."""

import sys
import time

import torch

x = torch.ones((int(sys.argv[1]) << 20,), dtype=torch.uint8, device="cuda")
print("holding", flush=True)
while True:
    time.sleep(1)
