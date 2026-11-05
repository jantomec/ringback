// ringback-render: what a session shows you of a 3D model, rendered on the computer that runs it
// with OpenUSD's Hydra Storm (docs/design.md, "Showing you things"; render/README.md). The agent
// runs it.
//
//   ringback-render still MODEL OUT.png [WIDTH HEIGHT [FRAME]]
//       a picture of the model, framed whole, at its first frame (or FRAME, from 0); prints
//       its facts as JSON: {"animated", "frames", "fps", "start", "end", "up"}
//   ringback-render info
//       which renderer this is and what it opens: {"renderer", "formats"}
#include "gpu.h"
#include "still.h"

#include <climits>
#include <cstdio>
#include <cstdlib>
#include <string>

#if defined(__APPLE__)
#include <mach-o/dyld.h>
#else
#include <unistd.h>
#endif

// The folder this binary is in, its links followed.
static std::string here() {
    char path[PATH_MAX] = {0};
#if defined(__APPLE__)
    uint32_t size = sizeof path;
    if (_NSGetExecutablePath(path, &size) != 0) return ".";
#else
    if (readlink("/proc/self/exe", path, sizeof path - 1) < 0) return ".";
#endif
    char resolved[PATH_MAX];
    std::string self = realpath(path, resolved) ? resolved : path;
    return self.substr(0, self.find_last_of('/'));
}

// Adobe's glTF and STL plugins, which build.sh puts beside the binary. Set before OpenUSD reads
// its plugins, at its first use.
static void findPlugins() {
    std::string plugins = here() + "/plugins/plugin/usd";
    if (const char* already = getenv("PXR_PLUGINPATH_NAME"); already && *already) plugins += std::string(":") + already;
    setenv("PXR_PLUGINPATH_NAME", plugins.c_str(), 1);
    // A glTF's materials as UsdPreviewSurface, which Storm draws without MaterialX.
    setenv("USD_FILEFORMATS_WRITE_OPENPBR", "0", 0);
}

static int usage() {
    fprintf(stderr, "usage: ringback-render still MODEL OUT.png [WIDTH HEIGHT [FRAME]] | info\n");
    return 2;
}

int main(int argc, char** argv) {
    if (argc < 2) return usage();
    findPlugins();
    const std::string command = argv[1];
    if (command == "info" && argc == 2) {
        printf("{\"renderer\": \"storm\", \"formats\": [\"usd\", \"usda\", \"usdc\", \"usdz\", \"glb\", \"gltf\", \"stl\"]}\n");
        return 0;
    }
    if (command == "still" && (argc == 4 || argc == 6 || argc == 7)) {
        const int width = argc >= 6 ? atoi(argv[4]) : 1200, height = argc >= 6 ? atoi(argv[5]) : 900;
        const int frame = argc == 7 ? atoi(argv[6]) : 0;
        if (width < 16 || height < 16 || width > 4096 || height > 4096 || frame < 0) return usage();
        std::string why;
        if (!startGPU(why)) {
            fprintf(stderr, "%s\n", why.c_str());
            return 1;
        }
        return still(argv[2], argv[3], width, height, frame);
    }
    return usage();
}
