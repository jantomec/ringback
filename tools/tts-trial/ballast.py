#!/usr/bin/env python3
"""Holds some GPU memory until killed, to see whether a model fits beside something else: a stand-in
for the speech-to-text model it would share the card with. Allocates through the CUDA driver's own
library, so it needs no packages. Usage: ballast.py MIB. Prints one line once it holds the memory."""
import ctypes
import sys
import time

BLOCK = 256 * 1024 * 1024
cuda = ctypes.CDLL("libcuda.so.1")


def call(name, *args):
    r = getattr(cuda, name)(*args)
    if r != 0:
        sys.exit(f"{name} failed: {r}")


call("cuInit", 0)
device, context = ctypes.c_int(), ctypes.c_void_p()
call("cuDeviceGet", ctypes.byref(device), 0)
call("cuCtxCreate_v2", ctypes.byref(context), 0, device)
want = int(sys.argv[1]) * 1024 * 1024
blocks = []
while len(blocks) * BLOCK < want:
    p = ctypes.c_ulonglong()
    call("cuMemAlloc_v2", ctypes.byref(p), ctypes.c_size_t(BLOCK))
    blocks.append(p)
print("holding", len(blocks) * 256, "MiB", flush=True)
while True:
    time.sleep(60)
