// `ringback-render still`: a picture of a model, from the view that frames it, for the
// notification that shows it to you.
#include "still.h"
#include "renderer.h"

#include <pxr/imaging/hio/image.h>

#include <cstdio>

// Writes 8-bit RGBA pixels, top row first, as a PNG, through OpenUSD's own image writer.
static bool writePNG(const std::string& path, std::vector<uint8_t>& pixels, int width, int height) {
    HioImageSharedPtr image = HioImage::OpenForWriting(path);
    if (!image) return false;
    HioImage::StorageSpec spec;
    spec.width = width;
    spec.height = height;
    spec.format = HioFormatUNorm8Vec4;
    spec.flipped = false;
    spec.data = pixels.data();
    return image->Write(spec);
}

int still(const std::string& path, const std::string& out, int width, int height, int frame) {
    Scene scene;
    std::string why;
    if (!openScene(path, scene, why)) {
        fprintf(stderr, "%s\n", why.c_str());
        return 1;
    }
    Renderer renderer(width, height);
    if (!renderer.ready(why)) {
        fprintf(stderr, "%s\n", why.c_str());
        return 1;
    }
    const View view = View::framing(scene, double(width) / height);
    HgiTextureHandle texture = renderer.render(scene, view, scene.timeOf(frame));
    if (!texture) {
        fprintf(stderr, "Storm rendered nothing\n");
        return 1;
    }
    std::vector<uint8_t> pixels = renderer.readRGBA8(texture);
    if (!writePNG(out, pixels, width, height)) {
        fprintf(stderr, "can't write %s\n", out.c_str());
        return 1;
    }
    printf("%s\n", scene.json().c_str());
    return 0;
}
