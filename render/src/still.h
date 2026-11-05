#pragma once

#include <string>

// Renders a picture of the model at path, at a frame (from 0), into the PNG at out, and prints
// the model's facts as JSON (Scene::json). 0 if it did; else 1, with why on stderr.
int still(const std::string& path, const std::string& out, int width, int height, int frame);
