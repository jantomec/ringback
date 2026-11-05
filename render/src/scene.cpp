#include "scene.h"

#include <pxr/base/tf/diagnosticMgr.h>
#include <pxr/usd/usdGeom/bboxCache.h>
#include <pxr/usd/usdGeom/metrics.h>
#include <pxr/usd/usdGeom/tokens.h>

#include <algorithm>
#include <cmath>
#include <cstdio>

int Scene::frames() const {
    return animated() ? std::max(1, (int)std::floor((end - start) / step() + 1e-6) + 1) : 1;
}

double Scene::timeOf(int frame) const {
    frame = std::clamp(frame, 0, frames() - 1);
    return std::min(end, start + frame * step());
}

std::string Scene::json() const {
    char text[256];
    snprintf(text, sizeof text,
             "{\"animated\": %s, \"frames\": %d, \"fps\": %g, \"start\": %g, \"end\": %g, \"up\": \"%s\"}",
             animated() ? "true" : "false", frames(), framesPerSecond, start, end, zUp ? "Z" : "Y");
    return text;
}

bool openScene(const std::string& path, Scene& scene, std::string& why) {
    TfErrorMark errors;
    scene.stage = UsdStage::Open(path);
    if (!scene.stage) {
        why = "can't open " + path;
        for (const TfError& error : errors) why += ": " + error.GetCommentary();
        errors.Clear();
        return false;
    }
    errors.Clear();
    UsdStageRefPtr stage = scene.stage;
    scene.zUp = UsdGeomGetStageUpAxis(stage) == UsdGeomTokens->z;
    scene.start = stage->GetStartTimeCode();
    scene.end = stage->GetEndTimeCode();
    scene.timeCodesPerSecond = stage->GetTimeCodesPerSecond();
    scene.framesPerSecond = stage->GetFramesPerSecond();
    // A glTF's time codes are seconds (one per second), while its frames are its stage's: a
    // frame is then a fraction of a time code.
    if (scene.timeCodesPerSecond <= 0) scene.timeCodesPerSecond = 24;
    if (scene.framesPerSecond <= 0) scene.framesPerSecond = scene.timeCodesPerSecond;
    UsdGeomBBoxCache cache(UsdTimeCode(scene.start), {UsdGeomTokens->default_, UsdGeomTokens->render});
    scene.bounds = cache.ComputeWorldBound(stage->GetPseudoRoot()).ComputeAlignedRange();
    if (scene.bounds.IsEmpty()) {
        why = path + " has nothing to draw";
        return false;
    }
    return true;
}

View View::framing(const Scene& scene, double aspect) {
    View view;
    view.target = scene.bounds.GetMidpoint();
    // Just far enough that each corner of the model's box is in the picture, with a margin.
    view.distance = 1;
    const GfVec3d back = (view.eye(scene.zUp) - view.target).GetNormalized();
    const GfVec3d right = GfCross(view.up(scene.zUp), back).GetNormalized();
    const GfVec3d up = GfCross(back, right);
    const double tanV = std::tan(view.fov * M_PI / 360), tanH = tanV * aspect;
    double distance = 1e-6;
    for (int i = 0; i < 8; ++i) {
        const GfVec3d corner = scene.bounds.GetCorner(i) - view.target;
        const double nearer = GfDot(corner, back);
        distance = std::max({distance, nearer + std::abs(GfDot(corner, right)) / tanH,
                             nearer + std::abs(GfDot(corner, up)) / tanV});
    }
    view.distance = distance * 1.08;
    return view;
}

GfVec3d View::eye(bool zUp) const {
    const double flat = distance * std::cos(elevation), rise = distance * std::sin(elevation);
    const GfVec3d offset = zUp ? GfVec3d(flat * std::sin(azimuth), -flat * std::cos(azimuth), rise)
                               : GfVec3d(flat * std::sin(azimuth), rise, flat * std::cos(azimuth));
    return target + offset;
}
