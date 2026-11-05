#include "gpu.h"

#if defined(__APPLE__)

bool startGPU(std::string&) { return true; }

#else

#include <EGL/egl.h>
#include <EGL/eglext.h>

// A display on the first GPU that EGL can open without a display server, or else EGL's default.
static EGLDisplay openDisplay() {
    auto queryDevices = reinterpret_cast<PFNEGLQUERYDEVICESEXTPROC>(eglGetProcAddress("eglQueryDevicesEXT"));
    auto platformDisplay = reinterpret_cast<PFNEGLGETPLATFORMDISPLAYEXTPROC>(eglGetProcAddress("eglGetPlatformDisplayEXT"));
    if (queryDevices && platformDisplay) {
        EGLDeviceEXT devices[8];
        EGLint count = 0;
        if (queryDevices(8, devices, &count)) {
            for (EGLint i = 0; i < count; ++i) {
                EGLDisplay display = platformDisplay(EGL_PLATFORM_DEVICE_EXT, devices[i], nullptr);
                EGLint major, minor;
                if (display != EGL_NO_DISPLAY && eglInitialize(display, &major, &minor)) return display;
            }
        }
    }
    EGLDisplay display = eglGetDisplay(EGL_DEFAULT_DISPLAY);
    EGLint major, minor;
    return display != EGL_NO_DISPLAY && eglInitialize(display, &major, &minor) ? display : EGL_NO_DISPLAY;
}

bool startGPU(std::string& why) {
    EGLDisplay display = openDisplay();
    if (display == EGL_NO_DISPLAY) {
        why = "EGL found no GPU to render on (an NVIDIA driver with EGL, or Mesa, is needed)";
        return false;
    }
    const EGLint wanted[] = {EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_BIT,
                             EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8, EGL_BLUE_SIZE, 8, EGL_ALPHA_SIZE, 8,
                             EGL_DEPTH_SIZE, 24, EGL_NONE};
    EGLConfig config;
    EGLint found = 0;
    if (!eglChooseConfig(display, wanted, &config, 1, &found) || found == 0 || !eglBindAPI(EGL_OPENGL_API)) {
        why = "EGL has no OpenGL configuration to render with";
        return false;
    }
    // Storm needs OpenGL 4.5; a compatibility profile, as usdview's.
    const EGLint version[] = {EGL_CONTEXT_MAJOR_VERSION, 4, EGL_CONTEXT_MINOR_VERSION, 5,
                              EGL_CONTEXT_OPENGL_PROFILE_MASK, EGL_CONTEXT_OPENGL_COMPATIBILITY_PROFILE_BIT, EGL_NONE};
    EGLContext context = eglCreateContext(display, config, EGL_NO_CONTEXT, version);
    if (context == EGL_NO_CONTEXT) {
        why = "EGL can't make an OpenGL 4.5 context, which Storm needs";
        return false;
    }
    // Storm renders into its own buffers; the surface only makes the context current.
    const EGLint size[] = {EGL_WIDTH, 16, EGL_HEIGHT, 16, EGL_NONE};
    EGLSurface surface = eglCreatePbufferSurface(display, config, size);
    if (!eglMakeCurrent(display, surface, surface, context)) {
        why = "EGL can't make its OpenGL context current";
        return false;
    }
    return true;
}

#endif
