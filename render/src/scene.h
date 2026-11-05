// A model as ringback-render shows it, and the view of it you control.
#pragma once

#include <pxr/pxr.h>
#include <pxr/base/gf/matrix4d.h>
#include <pxr/base/gf/range3d.h>
#include <pxr/base/gf/vec3d.h>
#include <pxr/usd/usd/stage.h>

#include <string>

PXR_NAMESPACE_USING_DIRECTIVE

// A model: its stage, how big it is and which way is up, and its animation if it has one.
struct Scene {
    UsdStageRefPtr stage;
    GfRange3d bounds;  // in world space, at its first time code
    bool zUp = false;
    double start = 0, end = 0;
    double timeCodesPerSecond = 24, framesPerSecond = 24;

    bool animated() const { return end > start; }
    // One frame, in time codes.
    double step() const { return timeCodesPerSecond / framesPerSecond; }
    // How many frames its animation has, the first and last included.
    int frames() const;
    // Frame i's time code, kept within its animation.
    double timeOf(int frame) const;
    // Its facts as JSON, for the agent: animation, frames and up axis.
    std::string json() const;
};

// Opens a model: USD (.usd, .usda, .usdc, .usdz), or glTF (.glb, .gltf) and STL through
// Adobe's file format plugins. False, with why, if it can't.
bool openScene(const std::string& path, Scene& scene, std::string& why);

// The view of a model: a camera orbiting a target point, at a distance, looking at it.
// Azimuth turns around the up axis; elevation tilts towards it (radians).
struct View {
    GfVec3d target{0, 0, 0};
    double azimuth = 0.6, elevation = 0.45, distance = 1;
    double fov = 40;  // vertical, in degrees

    // The view that frames the whole model, from three quarters in front and above.
    static View framing(const Scene& scene, double aspect);
    // Where the camera is, and which way is up, in the model's space.
    GfVec3d eye(bool zUp) const;
    GfVec3d up(bool zUp) const { return zUp ? GfVec3d(0, 0, 1) : GfVec3d(0, 1, 0); }
};
