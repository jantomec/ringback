#pragma once

#include <string>

// Readies what Storm renders with, before it starts: on Linux an OpenGL context of its own, through
// EGL, so it needs no display (on an NVIDIA card, EGL's device platform); on a Mac nothing, as
// Storm renders on Metal. False, with why, if it can't.
bool startGPU(std::string& why);
