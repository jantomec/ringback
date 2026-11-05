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

#include <unistd.h>
#if defined(__APPLE__)
#include <mach-o/dyld.h>
#endif

// This binary, its links followed.
static std::string self() {
    char path[PATH_MAX] = {0};
#if defined(__APPLE__)
    uint32_t size = sizeof path;
    if (_NSGetExecutablePath(path, &size) != 0) return ".";
#else
    if (readlink("/proc/self/exe", path, sizeof path - 1) < 0) return ".";
#endif
    char resolved[PATH_MAX];
    return realpath(path, resolved) ? resolved : path;
}

// Adobe's glTF and STL plugins, which build.sh puts beside the binary, on OpenUSD's plugin path.
// A monolithic OpenUSD, as Arch's, reads that path as it loads, before main, so the binary runs
// itself again with it set; it then finds it there.
static void findPlugins(char** argv) {
    const std::string binary = self();
    const std::string plugins = binary.substr(0, binary.find_last_of('/')) + "/plugins/plugin/usd";
    const char* already = getenv("PXR_PLUGINPATH_NAME");
    if (already && std::string(already).find(plugins) != std::string::npos) return;
    const std::string path = already && *already ? plugins + ":" + already : plugins;
    setenv("PXR_PLUGINPATH_NAME", path.c_str(), 1);
    // A glTF's materials as UsdPreviewSurface, which Storm draws without MaterialX.
    setenv("USD_FILEFORMATS_WRITE_OPENPBR", "0", 0);
    execv(binary.c_str(), argv);
    // If it can't, the plugins are left out: USD files still open.
}

static int usage() {
    fprintf(stderr, "usage: ringback-render still MODEL OUT.png [WIDTH HEIGHT [FRAME]] | info\n");
    return 2;
}

int main(int argc, char** argv) {
    if (argc < 2) return usage();
    findPlugins(argv);
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
