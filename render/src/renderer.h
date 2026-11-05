// Renders a model with OpenUSD's own renderer, Hydra Storm, headless: on Metal on a Mac, and on
// OpenGL through EGL elsewhere (gpu.h), with no window. Each frame is left in Storm's color
// texture, which a still reads back.
#pragma once

#include "scene.h"

#include <pxr/imaging/hgi/texture.h>

#include <cstdint>
#include <memory>
#include <vector>

PXR_NAMESPACE_OPEN_SCOPE
class UsdImagingGLEngine;
PXR_NAMESPACE_CLOSE_SCOPE

class Renderer {
public:
    Renderer(int width, int height);
    ~Renderer();

    int width() const { return width_; }
    int height() const { return height_; }
    // Whether Storm has a GPU to render on (Metal, or OpenGL through gpu.h); false with why if
    // not.
    bool ready(std::string& why) const;

    // Renders the model from the view at a time code, lit from the camera, into Storm's color
    // texture (RGBA, 16-bit float, bottom row first), which it returns.
    HgiTextureHandle render(const Scene& scene, const View& view, double time);
    // That texture, read back as 8-bit RGBA, top row first.
    std::vector<uint8_t> readRGBA8(HgiTextureHandle texture);

private:
    int width_, height_;
    std::unique_ptr<UsdImagingGLEngine> engine_;
};
