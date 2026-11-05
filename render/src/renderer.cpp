#include "renderer.h"

#include <pxr/base/arch/align.h>
#include <pxr/base/gf/camera.h>
#include <pxr/base/gf/frustum.h>
#include <pxr/base/gf/half.h>
#include <pxr/imaging/cameraUtil/framing.h>
#include <pxr/imaging/glf/simpleLight.h>
#include <pxr/imaging/glf/simpleMaterial.h>
#include <pxr/imaging/hd/tokens.h>
#include <pxr/imaging/hdx/tokens.h>
#include <pxr/imaging/hgi/blitCmds.h>
#include <pxr/imaging/hgi/blitCmdsOps.h>
#include <pxr/imaging/hgi/capabilities.h>
#include <pxr/usdImaging/usdImagingGL/engine.h>
#include <pxr/usd/usd/prim.h>
#include <pxr/usdImaging/usdImagingGL/renderParams.h>

#include <algorithm>
#include <cmath>

Renderer::Renderer(int width, int height) : width_(width), height_(height) {
    UsdImagingGLEngine::Parameters parameters;
    engine_ = std::make_unique<UsdImagingGLEngine>(parameters);
    // No presentation, which would draw into a window's framebuffer: the frame stays in Storm's
    // texture.
    engine_->SetEnablePresentation(false);
    engine_->SetRendererAov(HdAovTokens->color);
    engine_->SetFraming(CameraUtilFraming(GfRect2i(GfVec2i(0), width, height)));
    engine_->SetRenderBufferSize(GfVec2i(width, height));
}

Renderer::~Renderer() = default;

bool Renderer::ready(std::string& why) const {
    Hgi* hgi = engine_->GetHgi();
    if (!hgi || !hgi->IsBackendSupported()) {
        why = "Storm has no GPU to render on here";
        return false;
    }
    return true;
}

HgiTextureHandle Renderer::render(const Scene& scene, const View& view, double time) {
    const double radius = std::max(scene.bounds.GetSize().GetLength() * 0.5, 1e-6);
    GfCamera camera;
    camera.SetPerspectiveFromAspectRatioAndFieldOfView(float(width_) / height_, float(view.fov), GfCamera::FOVVertical);
    // Near enough to come close, far enough to keep the whole model behind it.
    const double nearest = std::max(view.distance - radius * 1.5, view.distance * 0.01);
    camera.SetClippingRange(GfRange1f(float(nearest), float(view.distance + radius * 2)));
    camera.SetTransform(GfMatrix4d().SetLookAt(view.eye(scene.zUp), view.target, view.up(scene.zUp)).GetInverse());
    const GfFrustum frustum = camera.GetFrustum();
    engine_->SetCameraState(frustum.ComputeViewMatrix(), frustum.ComputeProjectionMatrix());

    // A light at the camera, as usdview and usdrecord light a model with none of its own.
    const GfVec3d at = frustum.GetPosition();
    const GfVec4f ambient(0.01f, 0.01f, 0.01f, 1);
    GlfSimpleLight light(GfVec4f(float(at[0]), float(at[1]), float(at[2]), 1));
    light.SetTransform(frustum.ComputeViewInverse());
    light.SetAmbient(ambient);
    GlfSimpleMaterial material;
    material.SetAmbient(GfVec4f(0.2f, 0.2f, 0.2f, 1));
    material.SetSpecular(GfVec4f(0.1f, 0.1f, 0.1f, 1));
    material.SetShininess(32);
    engine_->SetLightingState({light}, material, ambient);

    UsdImagingGLRenderParams params;
    params.frame = UsdTimeCode(time);
    params.complexity = 1.0f;
    params.clearColor = GfVec4f(0.12f, 0.12f, 0.14f, 1);
    params.colorCorrectionMode = HdxColorCorrectionTokens->sRGB;
    do {
        engine_->Render(scene.stage->GetPseudoRoot(), params);
    } while (!engine_->IsConverged());
    return engine_->GetAovTexture(HdAovTokens->color);
}

std::vector<uint8_t> Renderer::readRGBA8(HgiTextureHandle texture) {
    Hgi* hgi = engine_->GetHgi();
    const HgiTextureDesc& described = texture->GetDescriptor();
    const int w = described.dimensions[0], h = described.dimensions[1];
    const size_t bytes = size_t(w) * h * HgiGetDataSizeOfFormat(described.format);
    const size_t page = hgi->GetCapabilities()->GetPageSizeAlignment();
    const size_t size = (bytes + page - 1) & ~(page - 1);
    void* buffer = ArchAlignedAlloc(page, size);
    HgiTextureGpuToCpuOp copy;
    copy.gpuSourceTexture = texture;
    copy.cpuDestinationBuffer = buffer;
    copy.destinationBufferByteSize = size;
    HgiBlitCmdsUniquePtr blit = hgi->CreateBlitCmds();
    blit->CopyTextureGpuToCpu(copy);
    hgi->SubmitCmds(blit.get(), HgiSubmitWaitTypeWaitUntilCompleted);

    std::vector<uint8_t> pixels(size_t(w) * h * 4);
    const bool half = described.format == HgiFormatFloat16Vec4;
    for (int y = 0; y < h; ++y) {
        const size_t from = size_t(h - 1 - y) * w * 4;
        uint8_t* out = pixels.data() + size_t(y) * w * 4;
        for (int x = 0; x < w * 4; ++x) {
            const float value = half ? float(static_cast<const GfHalf*>(buffer)[from + x])
                                     : static_cast<const uint8_t*>(buffer)[from + x] / 255.f;
            out[x] = (x % 4 == 3) ? 255 : uint8_t(std::clamp(value, 0.f, 1.f) * 255.f + 0.5f);
        }
    }
    ArchAlignedFree(buffer);
    return pixels;
}
